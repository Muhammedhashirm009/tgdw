package uploader

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const defaultFolderName = "telecloud"

type DriveUploader struct {
	client *drive.Service
}

// NewDriveUploader creates a new uploader using the provided OAuth2 token
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

// getOrCreateFolder finds a folder by name under the given parent, or creates it.
// If parentID is empty, it searches in the root ("root").
func (du *DriveUploader) getOrCreateFolder(folderName string, parentID string) (string, error) {
	if parentID == "" {
		parentID = "root"
	}

	// Search for existing folder
	query := "mimeType='application/vnd.google-apps.folder'" +
		" and name='" + folderName + "'" +
		" and '" + parentID + "' in parents" +
		" and trashed=false"

	result, err := du.client.Files.List().Q(query).Fields("files(id, name)").PageSize(1).Do()
	if err != nil {
		return "", err
	}

	if len(result.Files) > 0 {
		return result.Files[0].Id, nil
	}

	// Folder not found — create it
	folder := &drive.File{
		Name:     folderName,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentID},
	}

	created, err := du.client.Files.Create(folder).Fields("id").Do()
	if err != nil {
		return "", err
	}

	log.Printf("Created Google Drive folder '%s' (ID: %s)", folderName, created.Id)
	return created.Id, nil
}

type UploadProgressCallback func(bytesUploaded int64, totalBytes int64, speedBytesPerSec int64)

// Custom progress reader to track upload progress
type progressReader struct {
	io.Reader
	total                int64
	uploaded             int64
	lastReportedUploaded int64
	lastReportTime       time.Time
	callback             UploadProgressCallback
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	pr.uploaded += int64(n)

	now := time.Now()
	elapsed := now.Sub(pr.lastReportTime)

	if elapsed >= time.Second && pr.callback != nil {
		speed := int64(float64(pr.uploaded-pr.lastReportedUploaded) / elapsed.Seconds())
		pr.callback(pr.uploaded, pr.total, speed)
		pr.lastReportTime = now
		pr.lastReportedUploaded = pr.uploaded
	}

	return n, err
}

func (du *DriveUploader) UploadFile(ctx context.Context, filePath string, fileName string, callback UploadProgressCallback) (string, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return "", "", err
	}

	if fileName == "" {
		fileName = filepath.Base(filePath)
	}

	return du.UploadStream(ctx, file, fileName, stat.Size(), callback)
}

func (du *DriveUploader) UploadStream(ctx context.Context, reader io.Reader, fileName string, size int64, callback UploadProgressCallback) (string, string, error) {
	progressRdr := &progressReader{
		Reader:         reader,
		total:          size,
		lastReportTime: time.Now(),
		callback:       callback,
	}

	// Get or create the "telecloud" folder
	folderID, err := du.getOrCreateFolder(defaultFolderName, "")
	if err != nil {
		log.Printf("Warning: could not get/create '%s' folder, uploading to root: %v", defaultFolderName, err)
		// Fall back to uploading to root if folder creation fails
		folderID = ""
	}

	f := &drive.File{Name: fileName}
	if folderID != "" {
		f.Parents = []string{folderID}
	}

	res, err := du.client.Files.Create(f).Media(progressRdr).Context(ctx).Do()
	if err != nil {
		return "", "", err
	}

	// Create permission to make it shareable
	perm := &drive.Permission{
		Type: "anyone",
		Role: "reader",
	}
	_, err = du.client.Permissions.Create(res.Id, perm).Do()
	if err != nil {
		log.Printf("Warning: file uploaded but failed to set public permission: %v", err)
	}

	// Fetch the full file metadata to get the WebViewLink
	finalFile, err := du.client.Files.Get(res.Id).Fields("webViewLink").Do()
	if err != nil {
		// File is uploaded but we can't get the link — construct a fallback
		log.Printf("Warning: could not fetch webViewLink for file %s: %v", res.Id, err)
		fallbackLink := "https://drive.google.com/file/d/" + res.Id + "/view"
		return fallbackLink, res.Id, nil
	}

	return finalFile.WebViewLink, finalFile.Id, nil
}

func (du *DriveUploader) DeleteFile(fileID string) error {
	return du.client.Files.Delete(fileID).Do()
}
