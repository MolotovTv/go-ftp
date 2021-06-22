package ftp

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
	astilog "github.com/molotovtv/go-astilog"
	astiio "github.com/molotovtv/go-astitools/io"
	log "github.com/molotovtv/go-logger"
)

// FTP represents an FTP
type FTP struct {
	Addr          string
	Password      string
	Timeout       time.Duration
	Username      string
	dialer        Dialer
	persistent    bool
	ttl           time.Duration
	nextConnexion time.Time
	connexion     ServerConnexion
}

// New creates a new FTP connection based on a configuration
func New(c Configuration, dialer Dialer) *FTP {
	ftp := &FTP{
		Addr:       c.Addr,
		Password:   c.Password,
		Timeout:    c.Timeout,
		Username:   c.Username,
		dialer:     dialer,
		persistent: c.Persistent,
		ttl:        c.TTL,
	}

	if ftp.persistent {
		err := ftp.pconnect()
		if err != nil {
			log.Errorf("ftp persistent connexion fail : %+v", err)
		}
	}

	return ftp
}

// connect connects to the FTP and logs in
func (f *FTP) connect() (conn ServerConnexion, err error) {
	// Log
	l := fmt.Sprintf("FTP connect to %s with timeout %s", f.Addr, f.Timeout)
	log.Debugf("[Start] %s", l)
	defer func(now time.Time) {
		log.Debugf("[End] %s in %s", l, time.Since(now))
	}(time.Now())

	// Dial
	if f.Timeout > 0 {
		conn, err = f.dialer.DialTimeout(f.Addr, f.Timeout)
	} else {
		conn, err = f.dialer.Dial(f.Addr)
	}
	if err != nil {
		return conn, err
	}

	// Login
	if err = conn.Login(f.Username, f.Password); err != nil {
		conn.Quit()
		f.connexion = nil
	}
	return conn, err
}

func (f *FTP) quit(conn ServerConnexion) {
	if f.persistent == false {
		conn.Quit()
		f.connexion = nil
	}
}

// pconnect connects to the FTP and logs in
func (f *FTP) pconnect() (err error) {
	c, err := f.connect()
	if err != nil {
		return err
	}
	f.connexion = c
	f.nextConnexion = time.Now().Add(f.ttl * time.Second)
	return nil
}

// Connect connects to the FTP and logs in
func (f *FTP) Connect() (conn ServerConnexion, err error) {

	if f.persistent {
		if f.connexion == nil || f.nextConnexion.Unix() < time.Now().Unix() {
			err := f.pconnect()
			if err != nil {
				return f.connexion, err
			}
		}
		return f.connexion, nil
	}

	return f.connect()
}

// DownloadReader returns the reader built from the download of a file
func (f *FTP) DownloadReader(src string) (conn ServerConnexion, r io.ReadCloser, err error) {
	// Connect
	if conn, err = f.Connect(); err != nil {
		return conn, nil, err
	}

	// Download file
	if r, err = conn.Retr(src); err != nil {
		return conn, nil, err
	}
	return conn, r, nil
}

// Download downloads a file from the remote server
func (f *FTP) Download(ctx context.Context, src, dst string) (err error) {
	// Log
	l := fmt.Sprintf("FTP download from %s to %s", src, dst)
	log.Debugf("[Start] %s", l)
	defer func(now time.Time) {
		log.Debugf("[End] %s in %s", l, time.Since(now))
	}(time.Now())

	// Check context error
	if err = ctx.Err(); err != nil {
		return
	}

	// Connect
	var conn ServerConnexion
	if conn, err = f.Connect(); err != nil {
		return
	}
	defer f.quit(conn)

	// Check context error
	if err = ctx.Err(); err != nil {
		return
	}

	// Download file
	var r io.ReadCloser
	log.Debugf("Downloading %s", src)
	if r, err = conn.Retr(src); err != nil {
		return
	}
	defer r.Close()

	// Check context error
	if err = ctx.Err(); err != nil {
		return
	}

	// Create the destination file
	var dstFile *os.File
	log.Debugf("Creating %s", dst)
	if dstFile, err = os.Create(dst); err != nil {
		return
	}
	defer dstFile.Close()

	// Check context error
	if err = ctx.Err(); err != nil {
		return
	}

	// Copy to dst
	var n int64
	log.Debugf("Copying downloaded content to %s", dst)
	n, err = astiio.Copy(ctx, r, dstFile)
	log.Debugf("Copied %dkb", n/1024)
	return
}

// Remove removes a file
func (f *FTP) Remove(src string) (err error) {
	// Log
	l := fmt.Sprintf("FTP Remove of %s", src)
	log.Debugf("[Start] %s", l)
	defer func(now time.Time) {
		log.Debugf("[End] %s in %s", l, time.Since(now))
	}(time.Now())

	// Connect
	var conn ServerConnexion
	if conn, err = f.Connect(); err != nil {
		return
	}
	defer f.quit(conn)

	// Remove
	log.Debugf("Removing %s", src)
	if err = conn.Delete(src); err != nil {
		return
	}
	return
}

// Upload uploads a source path content to a destination
func (f *FTP) Upload(ctx context.Context, src, dst string) (err error) {
	// Log
	l := fmt.Sprintf("FTP Upload to %s", dst)
	log.Debugf("[Start] %s", l)
	defer func(now time.Time) {
		log.Debugf("[End] %s in %s", l, time.Since(now))
	}(time.Now())

	var srcFile *os.File
	log.Debugf("Opening %s", src)
	if srcFile, err = os.Open(src); err != nil {
		return
	}
	defer func() { _ = srcFile.Close() }()

	return f.UploadReader(ctx, srcFile, dst)
}

// UploadReader uploads a reader content to a destination
func (f *FTP) UploadReader(ctx context.Context, reader io.Reader, dst string) error {
	conn, err := f.Connect()

	if err != nil {
		return err
	}
	defer func() { f.quit(conn) }()

	// Check context error
	if err = ctx.Err(); err != nil {
		return err
	}

	log.Debugf("Uploading to %s", dst)
	return conn.Stor(dst, astiio.NewReader(ctx, reader))
}

// FileSize do
func (f *FTP) FileSize(src string) (s int64, err error) {
	// Log
	l := fmt.Sprintf("FTP file size of %s", src)
	log.Debugf("[Start] %s", l)
	defer func(now time.Time) {
		log.Debugf("[End] %s in %s", l, time.Since(now))
	}(time.Now())

	// Connect
	var conn ServerConnexion
	if conn, err = f.Connect(); err != nil {
		return
	}
	defer f.quit(conn)

	// File size
	return conn.FileSize(src)
}

// var FTPConnect = func(f *FTP) (conn *ftp.ServerConn, err error) {
// 	return nil, f.Connect
// }

//List do
func (f *FTP) List(sFolder string, aExtensionsAllowed []string, sPattern string) []*ftp.Entry {

	// Log
	l := fmt.Sprintf("FTP file list of %s", sFolder)
	astilog.Debugf("[Start] %s", l)
	defer func(now time.Time) {
		astilog.Debugf("[End] %s in %s", l, time.Since(now))
	}(time.Now())

	var aFiles, aFilesRaw []*ftp.Entry

	// Connect
	var conn ServerConnexion
	conn, err := f.Connect()
	if err != nil {
		log.Errorf("[FTP] error : %s", err.Error())
		return aFilesRaw
	}
	defer f.quit(conn)
	aFilesRaw, err = conn.List(sFolder)

	if err != nil {
		log.Errorf("[FTP] error : %s", err.Error())
		return aFiles
	}

	aExtensions := make(map[string]string)
	for _, sExtension := range aExtensionsAllowed {
		sExtension = strings.ToLower(sExtension)
		aExtensions[sExtension] = sExtension
	}
	bExtension := len(aExtensions) > 0

	bPattern := len(sPattern) > 0

	for _, oFile := range aFilesRaw {

		if oFile.Type != ftp.EntryTypeFile && oFile.Type != ftp.EntryTypeFolder {
			continue
		}

		aListToClean := map[string]string{".": ".", "..": ".."}
		_, ok := aListToClean[oFile.Name]
		if oFile.Type == ftp.EntryTypeFolder && ok {
			continue
		}

		sExtension := f.GetExtensionFile(oFile)
		if bExtension {
			if _, err := aExtensions[sExtension]; !err {
				continue
			}
		}

		if bPattern {
			if bMatch, _ := regexp.MatchString(sPattern, oFile.Name); !bMatch {
				continue
			}
		}

		aFiles = append(aFiles, oFile)
	}

	return aFiles
	// return nil
	// File size
	// return conn.ListFileSize(src)
}

//ListFolders do
func (f *FTP) ListFolders(sFolder string) []*ftp.Entry {

	// Log
	l := fmt.Sprintf("FTP list folder of %s", sFolder)
	astilog.Debugf("[Start] %s", l)
	defer func(now time.Time) {
		astilog.Debugf("[End] %s in %s", l, time.Since(now))
	}(time.Now())

	var aFolders, aFilesRaw []*ftp.Entry

	// Connect
	var conn ServerConnexion
	conn, err := f.Connect()
	if err != nil {
		log.Errorf("[FTP] error : %s", err.Error())
		return aFilesRaw
	}
	defer f.quit(conn)
	aFilesRaw, err = conn.List(sFolder)

	if err != nil {
		log.Errorf("[FTP] error : %s", err.Error())
		return aFolders
	}

	for _, oFile := range aFilesRaw {

		if oFile.Type != ftp.EntryTypeFolder {
			continue
		}

		aListToClean := map[string]string{".": ".", "..": ".."}
		_, ok := aListToClean[oFile.Name]
		if ok {
			continue
		}

		aFolders = append(aFolders, oFile)
	}

	return aFolders
}

//GetFileNameWithoutExtension do
func (f *FTP) GetFileNameWithoutExtension(sFileName string) string {
	aFileName := strings.Split(sFileName, ".")
	if len(aFileName) == 1 {
		return sFileName
	}
	return strings.Join(aFileName[:len(aFileName)-1], ".")
}

//GetExtensionFile do
func (f *FTP) GetExtensionFile(oFile *ftp.Entry) string {
	aFileName := strings.Split(oFile.Name, ".")
	sExtension := aFileName[len(aFileName)-1]
	return strings.ToLower(sExtension)
}

//Exists do
func (f *FTP) Exists(sFilePath string) (b bool, err error) {
	fmt.Println(sFilePath)
	// Log
	l := fmt.Sprintf("FTP file exists of %s", sFilePath)
	astilog.Debugf("[Start] %s", l)
	defer func(now time.Time) {
		astilog.Debugf("[End] %s in %s", l, time.Since(now))
	}(time.Now())

	// Connect
	var conn ServerConnexion
	if conn, err = f.Connect(); err != nil {
		return false, err
	}
	defer f.quit(conn)

	fmt.Println(conn.FileSize(sFilePath))

	if _, err := conn.FileSize(sFilePath); err != nil {
		return false, nil
	}

	return true, nil
}

//CreateDir do
func (f *FTP) CreateDir(sPath string) (err error) {

	// Connect
	var conn ServerConnexion
	if conn, err = f.Connect(); err != nil {
		return err
	}
	defer f.quit(conn)

	return conn.MakeDir(sPath)
}

//RemoveDir do
func (f *FTP) RemoveDir(sPath string) (err error) {

	// Connect
	var conn ServerConnexion
	if conn, err = f.Connect(); err != nil {
		return err
	}
	defer f.quit(conn)

	return conn.RemoveDir(sPath)
}

//RemoveDirRecur do
func (f *FTP) RemoveDirRecur(sPath string) (err error) {

	// Connect
	var conn ServerConnexion
	if conn, err = f.Connect(); err != nil {
		return err
	}
	defer f.quit(conn)

	return conn.RemoveDirRecur(sPath)
}

//Rename do
func (f *FTP) Rename(sSource string, sDestination string) (err error) {

	// Connect
	var conn ServerConnexion
	if conn, err = f.Connect(); err != nil {
		return err
	}
	defer f.quit(conn)

	aDestination := strings.Split(sDestination, "/")
	sDestinationFolder := strings.Join(aDestination[:len(aDestination)-1], "/")

	f.checkFolders(sDestinationFolder)

	return conn.Rename(sSource, sDestination)
}

func (f *FTP) checkFolders(sFolder string) {

	if len(sFolder) == 0 {
		return
	}

	ok, err := f.Exists(sFolder)
	if ok && err == nil {
		return
	}

	aFolder := strings.Split(sFolder, "/")

	if len(aFolder) == 2 {
		f.CreateDir(sFolder)
		return
	}

	f.checkFolders(strings.Join(aFolder[:len(aFolder)-1], "/"))
	f.CreateDir(sFolder)

}

//CreateFile in folder with content in param
func (f *FTP) CreateFile(sPath string, reader io.Reader) error {

	if len(sPath) == 0 {
		return nil
	}

	// Connect
	var conn ServerConnexion
	var err error

	if conn, err = f.Connect(); err != nil {
		return err
	}
	defer f.quit(conn)

	return conn.Stor(sPath, reader)

}
