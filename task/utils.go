package task

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/opengovern/og-util/pkg/es"
	"github.com/opensearch-project/opensearch-go/v2"
	"github.com/opensearch-project/opensearch-go/v2/opensearchapi"
	"golang.org/x/net/context"
	"io/ioutil"
)

func getCredsFromParams(params map[string][]string) Credentials {
	creds := Credentials{}
	for k, v := range params {
		switch k {
		case "github_username":
			if len(v) > 0 {
				creds.GithubUsername = v[0]
			}
		case "github_token":
			if len(v) > 0 {
				creds.GithubToken = v[0]
			}
		case "ecr_account_id":
			if len(v) > 0 {
				creds.ECRAccountID = v[0]
			}
		case "ecr_region":
			if len(v) > 0 {
				creds.ECRRegion = v[0]
			}
		case "acr_login_server":
			if len(v) > 0 {
				creds.ACRLoginServer = v[0]
			}
		case "acr_tenant_id":
			if len(v) > 0 {
				creds.ACRTenantID = v[0]
			}
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

func sendDataToOpensearch(client *opensearch.Client, doc es.Doc) error {
	docJSON, err := json.Marshal(doc)
	if err != nil {
		return err
	}

	keys, index := doc.KeysAndIndex()

	// Use the opensearchapi.IndexRequest to index the document
	req := opensearchapi.IndexRequest{
		Index:      index,
		DocumentID: es.HashOf(keys...),
		Body:       bytes.NewReader(docJSON),
		Refresh:    "true", // Makes the document immediately available for search
	}
	res, err := req.Do(context.Background(), client)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// Check the response
	if res.IsError() {
		return fmt.Errorf("error indexing document: %s", res.String())
	}
	return nil
}
