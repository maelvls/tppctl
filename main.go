package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

const (
	userAgent = "tppctl/v0.0.1"
)

func main() {
	tppURL := os.Getenv("TPP_URL")
	if tppURL == "" {
		fmt.Println("TPP_URL needs to be set in the environment")
		os.Exit(1)
	}

	token := os.Getenv("TOKEN")
	if token == "" {
		fmt.Println("TOKEN needs to be set in the environment")
		os.Exit(1)
	}

	listCmd := flag.NewFlagSet("ls", flag.ExitOnError)
	editCmd := flag.NewFlagSet("edit", flag.ExitOnError)

	if len(os.Args) < 2 {
		fmt.Println("Expected 'ls' or 'edit' subcommands")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "ls":
		listCmd.Parse(os.Args[2:])
		objs, err := listObjects(tppURL, token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		for _, obj := range objs {
			fmt.Println(obj)
		}
	case "edit":
		editCmd.Parse(os.Args[2:])
		if editCmd.NArg() < 1 {
			fmt.Println("Expected configuration name")
			os.Exit(1)
		}
		if err := editConfigInCred(tppURL, token, editCmd.Arg(0)); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

type Credential struct {
	Classname    string    `json:"Classname"`
	Contact      []Contact `json:"Contact"`
	FriendlyName string    `json:"FriendlyName"`
	Result       Result    `json:"Result"`
	Values       []Values  `json:"Values"`
}
type Contact struct {
	Prefix            string `json:"Prefix"`
	PrefixedName      string `json:"PrefixedName"`
	PrefixedUniversal string `json:"PrefixedUniversal"`
	State             int    `json:"State"`
	Universal         string `json:"Universal"`
}
type Values struct {
	Name  string `json:"Name"`
	Type  string `json:"Type"`
	Value string `json:"Value"`
}

func getCred(apiURL, token, credPath string) (*Credential, error) {
	body, err := json.Marshal(struct {
		CredentialPath string `json:"CredentialPath"`
	}{CredentialPath: credPath})
	if err != nil {
		return nil, fmt.Errorf("while marshalling request: %w", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/vedsdk/credentials/retrieve", apiURL), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("while creating request for /vedsdk/credentials/retrieve: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("while making request to /vedsdk/credentials/retrieve: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Dump body.
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("retrieve failed: %s, body: %v", resp.Status, string(body))
	}

	var cred Credential
	if err := json.NewDecoder(resp.Body).Decode(&cred); err != nil {
		return nil, fmt.Errorf("while decoding response from /vedsdk/credentials/retrieve: %w", err)
	}

	return &cred, nil
}

func editConfigInCred(tppURL, token, credPath string) error {
	credResp, err := getCred(tppURL, token, credPath)
	if err != nil {
		return err
	}
	switch Result(credResp.Result) {
	case ResultSuccess:
		// continue
	case ResultAttributeNotFound:
		return fmt.Errorf("attribute not found: '%s'", credPath)
	default:
		return fmt.Errorf("error fetching '%s': %v", credPath, ResultString(credResp.Result))
	}

	// Get the Values[0].Value, and base64-decode it. This is the YAML blob that
	// we want to edit.
	if len(credResp.Values) == 0 {
		return fmt.Errorf("no values found in '%s'", credPath)
	}
	yamlBlob, err := base64.StdEncoding.DecodeString(credResp.Values[0].Value)
	if err != nil {
		return fmt.Errorf("error base64-decoding the field 'Values[0].Value': %w", err)
	}

	tmpfile, err := os.CreateTemp("", "vcp-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmpfile.Name())
	if _, err := tmpfile.Write(yamlBlob); err != nil {
		return err
	}
	tmpfile.Close()

	// Open editor to let you edit YAML.
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	cmd := exec.Command(editor, tmpfile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	// Read and parse the modified YAML.
	yamlBlob, err = os.ReadFile(tmpfile.Name())
	if err != nil {
		return err
	}

	credResp.Values[0].Value = base64.StdEncoding.EncodeToString(yamlBlob)

	err = updateCred(tppURL, token, credPath, *credResp)
	if err != nil {
		return fmt.Errorf("while patching Firefly configuration: %w", err)
	}

	return nil
}

// POST /vedsdk/Credentials/update
func updateCred(tppURL, token, credPath string, c Credential) error {
	body, err := json.Marshal(struct {
		//inline
		Credential     `json:",inline"`
		CredentialPath string `json:"CredentialPath"`
	}{
		Credential:     c,
		CredentialPath: credPath,
	})
	if err != nil {
		return fmt.Errorf("while marshalling request for POST /vedsdk/credentials/update: %w", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/vedsdk/credentials/update", tppURL), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("while creating request for POST /vedsdk/credentials/update: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("while making request to POST /vedsdk/credentials/update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Dump body.
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update failed: %s, body: %v", resp.Status, string(body))
	}

	var res struct {
		Result Result `json:"Result"`
	}
	err = json.NewDecoder(resp.Body).Decode(&res)
	if err != nil {
		return err
	}

	switch res.Result {
	case ResultSuccess:
		// continue
	case ResultAttributeNotFound:
		return fmt.Errorf("attribute not found: %q", credPath)
	default:
		return fmt.Errorf("error updating %q: %v", credPath, ResultString(res.Result))
	}
	return nil
}

func listObjects(tppURL, token string) ([]string, error) {
	body, err := json.Marshal(struct {
		ObjectDN  string `json:"ObjectDN"`
		Pattern   string `json:"Pattern"`
		Recursive bool   `json:"Recursive"`
	}{
		ObjectDN:  `\VED\Policy`,
		Recursive: true,
	})
	if err != nil {
		return nil, fmt.Errorf("while marshalling request for /vedsdk/Config/Enumerate: %w", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/vedsdk/Config/Enumerate", tppURL), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("while creating request for /vedsdk/Config/Enumerate: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("while making request to /vedsdk/Config/Enumerate: %w", err)
	}
	defer resp.Body.Close()

	var creds ObjectsResp
	err = json.NewDecoder(resp.Body).Decode(&creds)
	if err != nil {
		return nil, fmt.Errorf("while decoding response from /vedsdk/Config/Enumerate: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Dump body.
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list failed: %s: %s", resp.Status, string(body))
	}

	var credPaths []string
	for _, cred := range creds.Objects {
		if !strings.Contains(cred.TypeName, "Generic Credential") {
			continue
		}
		credPaths = append(credPaths, fmt.Sprintf("%s", cred.Dn))
	}
	return credPaths, nil
}

type ObjectsResp struct {
	Objects []Objects `json:"Objects"`
	Result  int       `json:"Result"`
}
type Objects struct {
	AbsoluteGUID string `json:"AbsoluteGUID"`
	Dn           string `json:"DN"`
	GUID         string `json:"GUID"`
	ID           int    `json:"Id"`
	Name         string `json:"Name"`
	Parent       string `json:"Parent"`
	Revision     int    `json:"Revision"`
	TypeName     string `json:"TypeName"`
}

type TypeName string

const (
	TypeNameX509ServerCert       TypeName = "X509 Server Certificate"
	TypeNamePolicy               TypeName = "Policy"
	TypeNameGoogleCred           TypeName = "Google Credential"
	TypeNameGenericCred          TypeName = "Generic Credential"
	TypeNameUsernamePasswordCred TypeName = "Username Password Credential"
)
