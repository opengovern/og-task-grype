package task

import (
	"fmt"
	"io/ioutil"
)

func getCredsFromParams(params map[string]string) Credentials {
	creds := Credentials{}
	for k, v := range params {
		switch k {
		case "github_username":
			creds.GithubUsername = v
		case "github_token":
			creds.GithubToken = v
		case "ecr_account_id":
			creds.ECRAccountID = v
		case "ecr_region":
			creds.ECRRegion = v
		case "acr_login_server":
			creds.ACRLoginServer = v
		case "acr_tenant_id":
			creds.ACRTenantID = v
		}
	}
	return creds
}

func showFiles(dir string) error {
	// List the files in the current directory
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return err
	}

	// Print each file or directory name
	fmt.Printf("Listing files in directory: %s\n", dir)
	for _, file := range files {
		if file.IsDir() {
			fmt.Printf("[DIR] %s\n", file.Name())
		} else {
			fmt.Printf("[FILE] %s\n", file.Name())
		}
	}
	return nil
}
