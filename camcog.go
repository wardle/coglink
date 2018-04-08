//
// Camcog URL service
//
// Generates a unique URL for a set of cognitive questionnaires using the CAMCOG web service.
//
// See API documentation at https://cantab.atlassian.net/wiki/spaces/API/overview
// and https://cantab.atlassian.net/wiki/spaces/API/pages/137987972/Generating+a+Subject+URL
//
// This is designed to run from the command-line, taking in a single subject identifier
// or processing a list of identifiers from a CSV file. It could also be used to
// create a custom web service that simply redirects to the questionnaire.
//
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
)

const (
	defaultBaseURL   = "connect_demo.int.cantab.com"
	defaultUserAgent = "eldrix-camcog/1"
	version          = 0.1
)

var flagConfig = flag.String("config", "config.yml", "Location of configuration file. Default config.yml in current directory, /etc/ or ~/.camcog/")
var flagPassword = flag.String("password", "", "password")
var flagSubject = flag.String("subject", "", "local subject identifier")
var flagProcess = flag.String("csv", "", "Process a CSV containing identifiers in the first column")
var flagVersion = flag.Bool("version", false, "Prints version information")

func main() {
	// bring in command-line flags into our configuration
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)
	// configure config files and environmental variables
	viper.SetEnvPrefix("camcog") // will be uppercased automatically
	viper.AutomaticEnv()
	viper.SetDefault("UserAgent", defaultUserAgent)
	viper.SetConfigName("config")
	viper.AddConfigPath("/etc/appname/") // path to look for the config file in
	viper.AddConfigPath("$HOME/.camcog") // call multiple times to add many search paths
	viper.AddConfigPath(".")             // optionally look for config in the working directory
	if *flagConfig != "" {
		viper.AddConfigPath(*flagConfig)
	}
	err := viper.ReadInConfig() // Find and read the config file
	if err != nil {             // Handle errors reading the config file
		panic(fmt.Errorf("fatal error config file: %s", err))
	}
	cc, err := NewCamcog(
		viper.GetString("baseURL"),
		viper.GetString("username"),
		viper.GetString("password"),
		viper.GetString("userAgent"),
	)
	if err != nil {
		log.Fatal(err)
	}
	if *flagVersion {
		fmt.Printf("camcog URL generator V%v\n", version)
		os.Exit(0)
	}
	if *flagSubject != "" {
		url, err := processSingleSubject(cc, *flagSubject)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(url)
		os.Exit(0)
	}
	if *flagProcess != "" {
		processCsv(cc, *flagProcess)
		os.Exit(0)
	}
	flag.PrintDefaults()
	os.Exit(1)
}

func processCsv(cc *Camcog, filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		return err
	}
	for _, record := range records {
		id := record[0]
		email := record[1]
		url, err := processSingleSubject(cc, id)
		if err != nil {
			return err
		}
		fmt.Printf("%s,%s,%s\n", id, email, url)
	}
	return nil
}

func processSingleSubject(cc *Camcog, subject string) (string, error) {
	sr, err := cc.GetSubject(
		viper.GetString("groupDef"),
		viper.GetString("organisation"),
		viper.GetString("studyID"),
		viper.GetString("site"),
		viper.GetString("studyDef"),
		subject,
	)
	if err != nil {
		log.Fatal(err)
	}
	if len(sr.Records) == 1 {
		subject := sr.Records[0].ID
		sli, err := cc.GenerateSubjectAccessCode(subject)
		if err != nil {
			log.Fatal(err)
		}
		if len(sli.Records) != 1 {
			log.Fatalf("did not get access code for subject %s", subject)
		}
		url := cc.GenerateURL(subject, sli.Records[0].AccessCode)
		return url, nil
	}
	return "", errors.New("no access code generated from remote service")
}

// StatusError represents an error on the service-side
type StatusError struct {
	Code int
	Err  error
}

func checkStatusError(res *http.Response) error {
	if res.StatusCode < 300 {
		return nil
	}
	return &StatusError{
		Code: res.StatusCode,
		Err:  fmt.Errorf("error %s", res.Status),
	}
}

func (se StatusError) Error() string {
	return se.Err.Error()
}

// Camcog encapsulates the remote REST camcog service
type Camcog struct {
	baseURL    *url.URL
	httpClient *http.Client
	username   string
	password   string
	userAgent  string
}

// NewCamcog creates a new service client using the specified configuration.
func NewCamcog(baseURL string, username string, password string, userAgent string) (*Camcog, error) {
	url, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	return &Camcog{
		baseURL:    url,
		httpClient: http.DefaultClient,
		username:   username,
		password:   password,
		userAgent:  userAgent,
	}, nil
}

// GetSubject either fetches an existing subject or registers a new one using the subject identifier specified.
func (cc Camcog) GetSubject(groupDef string, org string, studyID string, site string, studyDef string, subjectID string) (*SubjectsResponse, error) {
	sr, err := cc.getSubject(studyID, subjectID)
	if err != nil {
		return nil, err
	}
	if len(sr.Records) == 0 {
		sr, err = cc.createSubject(groupDef, org, studyID, site, studyDef, subjectID)
	}
	return sr, err
}

// getSubject returns a subject, or an empty response if that subject is not already registered.
func (cc Camcog) getSubject(studyID string, subjectID string) (*SubjectsResponse, error) {
	params := make(map[string]string)
	params["limit"] = "1"
	params["filter"] = fmt.Sprintf("{\"study\":\"%s\",subjectIds=\"%s\"}", studyID, subjectID)
	req, err := cc.newRequest("GET", "/api/subject", nil, params)
	req.SetBasicAuth(cc.username, cc.password)
	var sres SubjectsResponse
	res, err := cc.do(req, &sres)
	if err != nil {
		return nil, err
	}
	return &sres, checkStatusError(res)
}

// createSubject creates a new subject, failing if that subject is already registered.
func (cc Camcog) createSubject(groupDef string, org string, studyID string, site string, studyDef string, subjectID string) (*SubjectsResponse, error) {
	csr := &CreateSubjectRequest{
		GroupDef:     groupDef,
		Organisation: org,
		Site:         site,
		Status:       "NEW",
		Study:        studyID,
		StudyDef:     studyDef,
		SubjectIds:   []string{subjectID},
	}
	req, err := cc.newRequest("POST", "/server-webservices/subject", csr, nil)
	req.SetBasicAuth(cc.username, cc.password)
	var sres SubjectsResponse
	res, err := cc.do(req, &sres)
	if err != nil {
		return nil, err
	}
	return &sres, checkStatusError(res)
}

// GenerateURL generates a URL for the subject to complete their questionnaires
func (cc Camcog) GenerateURL(subject string, accesscode string) string {
	return fmt.Sprintf("https://app.cantab.com/subject/index.html?accessCode=%s&subject=%s", accesscode, subject)
}

// GenerateSubjectAccessCode generates an access code for the subject specified
func (cc Camcog) GenerateSubjectAccessCode(subjectUUID string) (*SubjectLoginInfo, error) {
	params := make(map[string]string)
	params["limit"] = "1"
	params["filter"] = fmt.Sprintf("{\"subject\":\"%s\"}", subjectUUID)
	req, err := cc.newRequest("GET", "/server-webservices/subjectLoginInfo", nil, params)
	req.SetBasicAuth(cc.username, cc.password)
	var sli SubjectLoginInfo
	res, err := cc.do(req, &sli)
	if err != nil {
		return nil, err
	}
	return &sli, checkStatusError(res)
}

func (cc *Camcog) newRequest(method, path string, body interface{}, params map[string]string) (*http.Request, error) {
	rel := &url.URL{Path: path}
	u := cc.baseURL.ResolveReference(rel)
	if len(params) > 0 {
		q := u.Query()
		for k, v := range params {
			q.Add(k, v)
		}
		u.RawQuery = q.Encode()
	}
	var buf io.ReadWriter
	if body != nil {
		buf = new(bytes.Buffer)
		err := json.NewEncoder(buf).Encode(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, u.String(), buf)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", cc.userAgent)
	return req, nil
}

func (cc *Camcog) do(req *http.Request, v interface{}) (*http.Response, error) {
	resp, err := cc.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		err = json.NewDecoder(resp.Body).Decode(v)
	}
	return resp, err
}

// CreateSubjectRequest is used to generate a new subject
type CreateSubjectRequest struct {
	SubjectIds   []string `json:"subjectIds"`
	GroupDef     string   `json:"groupDef"`
	Site         string   `json:"site"`
	Study        string   `json:"study"`
	StudyDef     string   `json:"studyDef"`
	Organisation string   `json:"organisation"`
	Status       string   `json:"status"`
}

// SubjectsResponse is returned from API endpoints
// https://connect-demo.int.cantab.com/api/subject    (GET)
// https://connect-demo.int.cantab.com/server-webservices/subject   (POST)
// Generated using https://mholt.github.io/json-to-go/
type SubjectsResponse struct {
	Records []struct {
		ClientID        interface{}   `json:"clientId"`
		GroupDef        string        `json:"groupDef"`
		Locale          interface{}   `json:"locale"`
		Organisation    string        `json:"organisation"`
		ReplacedBy      interface{}   `json:"replacedBy"`
		Replicas        []interface{} `json:"replicas"`
		ScreeningStatus interface{}   `json:"screeningStatus"`
		Site            string        `json:"site"`
		Status          string        `json:"status"`
		Study           string        `json:"study"`
		StudyDef        string        `json:"studyDef"`
		SubjectIds      []string      `json:"subjectIds"`
		SubjectItems    []struct {
			ClientID       interface{} `json:"clientId"`
			SubjectItemDef string      `json:"subjectItemDef"`
			ID             string      `json:"id"`
			Text           interface{} `json:"text"`
			MultiText      interface{} `json:"multiText"`
			Date           interface{} `json:"date"`
			Integer        interface{} `json:"integer"`
			Locale         string      `json:"locale"`
			HidesPII       bool        `json:"hidesPII"`
		} `json:"subjectItems"`
		ID      string `json:"id"`
		Version int    `json:"version"`
	} `json:"records"`
	Total   int  `json:"total"`
	Success bool `json:"success"`
}

// SubjectLoginInfo provides login information for a given patient
type SubjectLoginInfo struct {
	Records []struct {
		AccessCode               string      `json:"accessCode"`
		AccessCodeCreationDate   int64       `json:"accessCodeCreationDate"`
		AccessCodeCreatorID      interface{} `json:"accessCodeCreatorId"`
		AccessCodeCreatorType    interface{} `json:"accessCodeCreatorType"`
		ClientID                 interface{} `json:"clientId"`
		LastSubjectLinkEmailSent interface{} `json:"lastSubjectLinkEmailSent"`
		Organisation             string      `json:"organisation"`
		Site                     string      `json:"site"`
		Study                    string      `json:"study"`
		Subject                  string      `json:"subject"`
		ID                       string      `json:"id"`
		Version                  int         `json:"version"`
	} `json:"records"`
	Total   int  `json:"total"`
	Success bool `json:"success"`
}

// StudyDefinitionResponse is returned from API endpoint https://connect-demo.int.cantab.com/api/studyDef
type StudyDefinitionResponse struct {
	Records []struct {
		ClientID        string        `json:"clientId"`
		DataEnrichments []interface{} `json:"dataEnrichments"`
		GroupDefs       []struct {
			AllocationParameters []struct {
				ClientID   interface{} `json:"clientId"`
				Method     string      `json:"method"`
				StimuliSet string      `json:"stimuliSet"`
				TestCode   string      `json:"testCode"`
				ID         string      `json:"id"`
			} `json:"allocationParameters"`
			ClientID  string `json:"clientId"`
			Name      string `json:"name"`
			VisitDefs []struct {
				CanBeSelfAdministered   interface{}   `json:"canBeSelfAdministered"`
				ClientID                string        `json:"clientId"`
				ConditionalReleaseTexts []interface{} `json:"conditionalReleaseTexts"`
				Description             string        `json:"description"`
				ItemGroupDefs           []struct {
					ClientID           string      `json:"clientId"`
					FirstPeerTestDefID interface{} `json:"firstPeerTestDefId"`
					Mode               string      `json:"mode"`
					Precondition       interface{} `json:"precondition"`
					PreconditionAction interface{} `json:"preconditionAction"`
					TestCode           string      `json:"testCode"`
					TestExecutionDefID interface{} `json:"testExecutionDefId"`
					ID                 string      `json:"id"`
				} `json:"itemGroupDefs"`
				Name                      string      `json:"name"`
				Optional                  bool        `json:"optional"`
				RequiredSubjectIdentifier interface{} `json:"requiredSubjectIdentifier"`
				UpdateSubjectStatusTo     interface{} `json:"updateSubjectStatusTo"`
				VisitID                   string      `json:"visitId"`
				ID                        string      `json:"id"`
			} `json:"visitDefs"`
			ID string `json:"id"`
		} `json:"groupDefs"`
		Organisation               string      `json:"organisation"`
		ParentStudyDef             interface{} `json:"parentStudyDef"`
		PerformanceObservationsDef struct {
			ClientID interface{} `json:"clientId"`
			Enabled  bool        `json:"enabled"`
			ID       string      `json:"id"`
		} `json:"performanceObservationsDef"`
		SelfAdministrationDef struct {
			AutoCreateSubjectLogins                   bool     `json:"autoCreateSubjectLogins"`
			ClientID                                  string   `json:"clientId"`
			ConsentText                               string   `json:"consentText"`
			EditDetails                               bool     `json:"editDetails"`
			PermitAllTasks                            bool     `json:"permitAllTasks"`
			PermittedDevices                          []string `json:"permittedDevices"`
			ReleaseText                               string   `json:"releaseText"`
			SelfRegistrationEnabled                   bool     `json:"selfRegistrationEnabled"`
			ShowConsentMessageToPreRegisteredSubjects bool     `json:"showConsentMessageToPreRegisteredSubjects"`
			ID                                        string   `json:"id"`
		} `json:"selfAdministrationDef"`
		SequenceNumber int    `json:"sequenceNumber"`
		Status         string `json:"status"`
		Study          string `json:"study"`
		SubjectDataDef struct {
			ClientID              interface{} `json:"clientId"`
			SubjectIdentifierDefs []struct {
				ClientID string      `json:"clientId"`
				Format   string      `json:"format"`
				HelpText string      `json:"helpText"`
				Label    string      `json:"label"`
				Prefix   interface{} `json:"prefix"`
				ID       string      `json:"id"`
			} `json:"subjectIdentifierDefs"`
			SubjectItemDefs []struct {
				ClientID interface{} `json:"clientId"`
				HelpText string      `json:"helpText"`
				ItemSpec struct {
					ClientID interface{} `json:"clientId"`
					Locales  []string    `json:"locales"`
					ID       string      `json:"id"`
				} `json:"itemSpec"`
				Label               string      `json:"label"`
				PatientIdentifiable interface{} `json:"patientIdentifiable"`
				RequireConfirmation bool        `json:"requireConfirmation"`
				Required            bool        `json:"required"`
				Type                string      `json:"type"`
				ID                  string      `json:"id"`
			} `json:"subjectItemDefs"`
			ID string `json:"id"`
		} `json:"subjectDataDef"`
		Terminology struct {
			ClientID interface{} `json:"clientId"`
			Group    string      `json:"group"`
			Site     string      `json:"site"`
			Study    string      `json:"study"`
			Subject  string      `json:"subject"`
			Visit    string      `json:"visit"`
			ID       string      `json:"id"`
		} `json:"terminology"`
		ValidationWarnings []struct {
			ClientID   interface{} `json:"clientId"`
			TestCode   string      `json:"testCode"`
			TestDef    string      `json:"testDef"`
			WarningKey string      `json:"warningKey"`
			ID         string      `json:"id"`
			Type       string      `json:"type"`
		} `json:"validationWarnings"`
		VersionName           string        `json:"versionName"`
		ID                    string        `json:"id"`
		Version               int           `json:"version"`
		CreationDateTime      int64         `json:"creationDateTime"`
		ReasonForCreation     string        `json:"reasonForCreation"`
		DataEnrichmentPalette []interface{} `json:"dataEnrichmentPalette"`
	} `json:"records"`
	Total   int  `json:"total"`
	Success bool `json:"success"`
}
