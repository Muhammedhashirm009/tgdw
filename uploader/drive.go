package uploader

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type DriveUploader struct {
	client *drive.Service
}

// NewDriveUploader creates a new uploader using the provided OAuth2 token
// Demonstrating the logic for Method 1 / Method 2 as per skills.md
func NewDriveUploader(ctx context.Context, token *oauth2.Token, clientID, clientSecret string) (*DriveUploader, error) {
	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		Scopes: []string{drive.DriveFileScope},
	}
	
	client := config.Client(ctx, token)
	
	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	
	return &DriveUploader{client: srv}, nil
}

type UploadProgressCallback func(bytesUploaded int64, totalBytes int64)

// Custom progress reader to track upload progress
type progressReader struct {
	io.Reader
	total    int64
	uploaded int64
	callback UploadProgressCallback
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	pr.uploaded += int64(n)
	if pr.callback != nil {
		pr.callback(pr.uploaded, pr.total)
	}
	return n, err
}

func (du *DriveUploader) UploadFile(filePath string, callback UploadProgressCallback) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	
	stat, err := file.Stat()
	if err != nil {
		return "", err
	}
	
	reader := &progressReader{
		Reader:   file,
		total:    stat.Size(),
		callback: callback,
	}

	f := &drive.File{Name: filepath.Base(filePath)}
	res, err := du.client.Files.Create(f).Media(reader).Do()
	if err != nil {
		return "", err
	}
	
	// Create permission to make it shareable
	perm := &drive.Permission{
		Type: "anyone",
		Role: "reader",
	}
	_, err = du.client.Permissions.Create(res.Id, perm).Do()
	if err != nil {
		return "", err // It's still uploaded, but we failed to make it shareable
	}

	// Fetch the full file metadata to get the WebViewLink
	finalFile, err := du.client.Files.Get(res.Id).Fields("webViewLink").Do()
	if err != nil {
		return fmt.Sprintf("https://drive.google.com/file/d/%s/view", res.Id), nil
	}
	
	return finalFile.WebViewLink, nil
}
