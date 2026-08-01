package uploader

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const defaultFolderName = "telecloud"

type DriveUploader struct {
	client *drive.Service
}

// NewDriveUploader creates a new uploader using provided OAuth2 credentials with automatic token refresh
func NewDriveUploader(ctx context.Context, token *oauth2.Token, clientID, clientSecret string) (*DriveUploader, error) {
	if clientID == "" {
		clientID = os.Getenv("GOOGLE_CLIENT_ID")
	}
	if clientSecret == "" {
		clientSecret = os.Getenv("GOOGLE_CLIENT_SECRET")
	}

	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		Scopes: []string{drive.DriveFileScope},
	}

	// Create reusable TokenSource
	tokenSource := config.TokenSource(ctx, token)

	// Proactively verify & refresh token if expired
	freshToken, err := tokenSource.Token()
	if err == nil && freshToken != nil {
		token = freshToken
	} else if err != nil {
		log.Printf("Notice: TokenSource refresh check: %v (attempting fallback)", err)
	}

	highSpeedTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 60 * time.Second,
		}).DialContext,
		MaxIdleConns:          500,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		ReadBufferSize:        8 * 1024 * 1024, // 8MB Socket Buffer
		WriteBufferSize:       8 * 1024 * 1024, // 8MB Socket Buffer
	}
	ctxWithClient := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: highSpeedTransport})
	httpClient := oauth2.NewClient(ctxWithClient, config.TokenSource(ctxWithClient, token))

	srv, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, err
	}

	return &DriveUploader{client: srv}, nil
}

// getOrCreateFolder finds a folder by name under the given parent, or creates it.
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

	if (elapsed >= time.Second || err == io.EOF || pr.uploaded == pr.total) && pr.callback != nil {
		speed := int64(0)
		if elapsed.Seconds() > 0 {
			speed = int64(float64(pr.uploaded-pr.lastReportedUploaded) / elapsed.Seconds())
		}
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
		folderID = ""
	}

	f := &drive.File{Name: fileName}
	if folderID != "" {
		f.Parents = []string{folderID}
	}

	res, err := du.client.Files.Create(f).Media(progressRdr, googleapi.ChunkSize(32*1024*1024)).Context(ctx).Do()
	if err != nil {
		return "", "", err
	}

	// Asynchronously set public permission without blocking the job completion
	go func(fID string) {
		perm := &drive.Permission{
			Type: "anyone",
			Role: "reader",
		}
		if _, pErr := du.client.Permissions.Create(fID, perm).Do(); pErr != nil {
			log.Printf("Notice: async permission set for %s: %v", fID, pErr)
		}
	}(res.Id)

	driveLink := "https://drive.google.com/file/d/" + res.Id + "/view"
	return driveLink, res.Id, nil
}

func (du *DriveUploader) DeleteFile(fileID string) error {
	return du.client.Files.Delete(fileID).Do()
}
