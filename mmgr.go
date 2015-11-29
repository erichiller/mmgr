package main

import p "github.com/variab1e/lgr"
import cli "github.com/spf13/cobra"
import "github.com/dhowden/tag"
import "database/sql"
import _ "github.com/mattn/go-sqlite3" // stub package the prepended _ indicated this
import googm "github.com/lxr/go.google.musicmanager"
import "golang.org/x/oauth2"
import "golang.org/x/oauth2/google"
import "github.com/cheggaaa/pb"

import "os"
import "strings"
import "strconv"
import "time"
import "io"
import "net/http"
import "encoding/json"
import "fmt"

const VERSION string = "mmgr v0.2"              // program title
var VERBOSE = true                              // default to true for startup, then drop
const DBPATH = "f.DB"                           // sqlite3 DB name / path
var MUSIC_EXT = []string{"mp3", "flac"}         // valid music file extensions
var OAUTH_CREDENTIAL_FILE = "./googm_oauth.json"// where to store gmusic oauth credentials

// ROOTCMD - root command setup, and the all important PersistentPreRun - without which the verbosity setting is useless.
var ROOTCMD = &cli.Command{
	Use: VERSION,
	PersistentPreRun: func(cmd *cli.Command, args []string) {
		setVerbosity()
	},
}
var DB *sql.DB               // declare DB to be GLOBAL
var ForceUpdate bool = false // whether to force update despite lack of file change

// Counters
var C_TOTAL int64 = 0
var C_UPDATED int64 = 0
var C_DIRS int64 = 0
var C_SKIPPED int64 = 0
var C_INVALID int64 = 0
var CUploaded int64 = 0			// total files successfully uploaded to google.

// OauthConfig - Google's official Music Manager's credentials
var OauthConfig = &oauth2.Config{
	ClientID:     "652850857958",
	ClientSecret: "ji1rklciNp2bfsFJnEH_i6al",
	RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
	Scopes:       []string{googm.Scope},
	Endpoint:     google.Endpoint,
}

var GOOGM_CREDENTIALS struct {
	ID string
	oauth2.Token
}

func init() {
	// log file?
	p.SetLogFile("mmgr.log")						// log to OAUTH_CREDENTIAL_FILE
	//lgr.UseTempLogFile("mmgr")					// temp log 
	setVerbosity()
	var cmdScan = &cli.Command{
		Use:   "scan [/path/to/directory/]",
		Short: "find music files",
		Long:  "Scan will scan the directory and list back all found music files",
		Run: func(cmd *cli.Command, args []string) {
			if len(args) > 0 {
				scanDir(args[0])
			} else {
				p.ERROR.Println("Not enough arguments for Scan.")
				p.ERROR.Println("Please see help for more details.")
			}
		},
	}
	var cmdUploadSrcdb = &cli.Command{
		Use:   "uploadsrcdb",
		Short: "upload music files with data already collected and stored in db",
		Long:  "Upload will load data from the database and upload all found music files that have changed",
		Run: func(cmd *cli.Command, args []string) {
			tracks, uploadFiles := scanDb()
			uploadTracks(tracks, uploadFiles)
		},
	}
	var cmdUpload = &cli.Command{
		Use:   "upload [/path/to/directory/]",
		Short: "upload music files",
		Long:  "Upload will scan the directory and upload all found music files that have changed",
		Run: func(cmd *cli.Command, args []string) {
			if len(args) > 0 {
				tracks, uploadFiles := scanDir(args[0])
				uploadTracks(tracks, uploadFiles)
			} else {
				p.ERROR.Println("Not enough arguments for Scan.")
				p.ERROR.Println("Please see help for more details.")
			}
		},
	}
	var cmdRegister = &cli.Command{
		Use:   "register",
		Short: "register with google music account by creating oauth token",
		Long: `` +
`Gmusic must be registered with the user's Google Play account before it
can be used to manage Google Play Music libraries.  The registration
process asks you to navigate to a special URL where you can grant access
permissions for the Google Play Music Manager.  Doing this gives you an
authorization code, which is then input to gmusic to register it.  Once
gmusic has been registered, it creates a file called ".gmusic.json" in
the user's home directory; other gmusic commands refer to this file for
their access credentials.

The ID under which you register gmusic in your Google Play Music library
needs to be unique on Google's side, so pick it reasonably randomly.
Remember that there are limits to how many devices a single account can
have authorized, with how many accounts a single device can be
authorized, and how many devices one account can deauthorize in a year,
so be careful in using this command.

Note that downloading tracks has been known to fail unless the ID is
sufficiently MAC address-like.  The exact threshold is unknown; perhaps
the server only checks for a colon.

If a human-readable name under which to register gmusic is not given,
it defaults to "gmusic".create oauth`,
		Run: func(cmd *cli.Command, args []string) {
			register()
		},
	}
	ROOTCMD.AddCommand(cmdScan, cmdUpload, cmdRegister, cmdUploadSrcdb)
}

func main() {
	openDB()         // open the database
	defer DB.Close() // defer it's eventual closing; here because apparently it closes at the end of the function it is declared in; thus not in openDB
	p.INFO.Println(VERSION)
	ROOTCMD.PersistentFlags().BoolVarP(&VERBOSE, "verbose", "v", false, "verbose output")
	ROOTCMD.PersistentFlags().BoolVarP(&ForceUpdate, "force_update", "f", false, "force update, even if the file has not changed")
	ROOTCMD.Execute()
	dispResults()
}

func uploadTracks(tracks []*googm.Track, uploadFiles []*os.File) error {
	p.INFO.Println("uploadTracks()-->")
	p.INFO.Printf("Received %v tracks", len(tracks))
	p.INFO.Printf("Received %v files", len(uploadFiles))
	client, err := loadGoogm()
	if err != nil {
		p.ERROR.Println("Failed to load google music manager lib:" + err.Error())
	}
	urls, errs := client.ImportTracks(tracks)
	var stmt *sql.Stmt
	p.INFO.Printf("%-15v %-35v %-15v %-15v %2v/%-6v\n","ClientId","Title","Album","Artist","##","T#","")
	for i, err := range errs {
		p.INFO.Printf("%-15v %-35v %-15v %-15v %-2v/%-6v\n%v",tracks[i].ClientId,tracks[i].Title,tracks[i].Album,tracks[i].Artist,tracks[i].TrackNumber,tracks[i].TotalTrackCount,uploadFiles[i].Name())
		var id string
		if err == nil {
			id, err = postTrack(urls[i], uploadFiles[i])
		}
		if err != nil {
			p.ERROR.Println(err)
			C_INVALID++
		} else {
			// EDH -- set as uploaded in sql
			query := "UPDATE files SET googmtime=?,googmid=? WHERE fullpath=?"
			stmt, err = DB.Prepare(query)
			if err != nil {
				p.ERROR.Println("The table update-prepare failed with error:" + err.Error())
			}
			_, err := stmt.Exec(time.Now().Unix(), tracks[i].ClientId, uploadFiles[i].Name())
			if err != nil {
				p.ERROR.Println("The table update-exec failed with error:" + err.Error())
			}
			CUploaded++
			p.INFO.Println("Successfully uploaded: " + id)
		}
	}
	return nil
}

func postTrack(url string, r io.Reader) (string, error) {
	// BUG(lor): Gmusic does not actually detect and report an error
	// on (most) non-MP3 files.  All files are uploaded to Google
	// Play, but only MP3 ones will be playable.
	p.TRACE.Println("postTrack upload to: " + url)
	resp, err := http.Post(url, "audio/mpeg", r)
	if err != nil {
		return "", err
	}
	return googm.CheckImportResponse(resp)
}

func scanDir(dir string) (tracks []*googm.Track, uploadFiles []*os.File) {
	// remove the trailing slash if present. I need uniformity.
	dir = strings.TrimRight(dir, "\\/")
	var err error
	p.INFO.Println("Scanning Directory: " + dir)
	folder, err := os.Open(dir)
	if err != nil {
		p.ERROR.Println("=====START-MESSAGE=====")
		p.ERROR.Println("Failed to Open [directory]: ]" + dir + "]")
		p.ERROR.Println("Error message: " + err.Error())
		p.ERROR.Println("Advice: Probably an invalid directory path used for music root location. See directory above listed between sqaure brackets. Ensure you are not escaping the trailing quote")
		p.ERROR.Println("=====END-MESSAGE=====")
		panic(err)
	}
	defer folder.Close()
	folderFiles, err := folder.Readdir(0)
	if err != nil {
		p.ERROR.Println("Error reading files in directory " + dir)
		p.ERROR.Println("Error message: " + err.Error())
		panic(err)
	}
	// EDH NOTE - not sure the SQL types to set googmid, googmtime, and sum to, may need to be varchar?
	_, err = DB.Exec(`CREATE TABLE IF NOT EXISTS ` + `files` + ` ( ` +
		`fullpath text not null primary key, ` +
		`directory text not null, ` +
		`filename text not null, ` +
		`extension text not null, ` +
		`filesize int not null, ` +
		`modtime int not null, ` +
		`googmid bigint, ` +
		`googmtime int, ` +
		`format text not null, ` +
		`filetype text not null, ` +
		`checksum bigint not null, ` +
		`title text, ` +
		`album text, ` +
		`artist text, ` +
		`album_artist text, ` +
		`composer text, ` +
		`genre text, ` +
		`year int, ` +
		`track_num int, ` +
		`track_total int, ` +
		`disc_num int, ` +
		`disc_total int, ` +
		`has_image bool not null, ` +
		`lyrics text` +
		`)`)
	if err != nil {
		p.ERROR.Println("The table creation failed with error:" + err.Error())
		panic(err)
	}
per_file_loop:
	for _, fileinfo := range folderFiles {
		C_TOTAL++
		p.TRACE.Println(fileinfo.Name() + " is dir=" + strconv.FormatBool(fileinfo.IsDir()) + " with size=" + strconv.FormatInt(fileinfo.Size(), 10) + " last modified on " + fileinfo.ModTime().String())
		fullpath := dir + "/" + fileinfo.Name()
		arr := strings.Split(fileinfo.Name(), ".")
		extension := arr[len(arr)-1]
		switch {
		case fileinfo.IsDir():
			C_DIRS++
			p.DEBUG.Println("file is in fact a directory; recursively scanning: " + fullpath)
			scanDir(fullpath)
			continue per_file_loop
		case !validateFile(fullpath):
			C_INVALID++
			p.WARN.Println("file is not valid music, skipping: " + fullpath)
			continue per_file_loop
		case !ForceUpdate && !shouldFileUpload(fullpath):
			C_SKIPPED++
			p.DEBUG.Println("File is unchanged since the last upload and will not be uploaded, unless overridden with -f")
			continue per_file_loop
		}
		C_UPDATED++
		// get a file handle (fh) for the upcoming tag read
		fh, err := os.Open(fullpath)
		if err != nil {
			p.ERROR.Println("FAILED OS READ: [file skipped] " + err.Error() + fullpath)
			C_INVALID++
			continue per_file_loop
		}
		//defer fh.Close()
		// get a checksum -- tag.Sum -- see tag/Sum.go func Sum() only does a checksum of media part of the file, that is the part after the tag, thus the checksum should stay constant despite changes to the tag of the file. The purpose for this is to create a STATIC UNIQUE IDENTIFIER for the file that is consistent across time.
		checksum, err := tag.Sum(fh)
		if err != nil {
			p.ERROR.Println("FAILED CHECKSUM READ: [file skipped] " + err.Error() + fullpath)
			C_INVALID++
			continue per_file_loop
		}
		// read the id tags
		t, err := tag.ReadFrom(fh)
		if err != nil {
			p.ERROR.Println("FAILED TAG READ: [file skipped] " + err.Error() + fullpath)
			C_INVALID++
			continue per_file_loop
		}

		var hasimage bool = false
		track_num, track_total := t.Track()
		disc_num, disc_total := t.Disc()
		if t.Picture() != nil {
			hasimage = true
		}
		track := &googm.Track{
			ClientId:        checksum,
			Title:           t.Title(),
			Album:           t.Album(),
			Artist:          t.Artist(),
			AlbumArtist:     t.AlbumArtist(),
			Composer:        t.Composer(),
			Year:            t.Year(),
			Genre:           t.Genre(),
			TrackNumber:     track_num,
			TotalTrackCount: track_total,
			DiscNumber:      disc_num,
			TotalDiscCount:  disc_total,
		}
		tracks = append(tracks, track)
		uploadFiles = append(uploadFiles, fh)
		stmt, err := DB.Prepare(`INSERT OR REPLACE INTO ` + `files` + `(` +
			`fullpath, ` +
			`directory, ` +
			`filename, ` +
			`extension, ` +
			`filesize, ` +
			`modtime, ` +
			`format, ` +
			`filetype, ` +
			`checksum, ` +
			`title, ` +
			`album, ` +
			`artist, ` +
			`album_artist, ` +
			`composer, ` +
			`genre, ` +
			`year, ` +
			`track_num, ` +
			`track_total, ` +
			`disc_num, ` +
			`disc_total, ` +
			`has_image, ` +
			`lyrics ` +
			`) VALUES ( ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? , ? );`)
		if err != nil {
			p.ERROR.Println("Query failed - could not insert data for file: " + fileinfo.Name())
		}
		_, err = stmt.Exec(
			fullpath,
			dir,
			fileinfo.Name(),
			extension,
			fileinfo.Size(),
			fileinfo.ModTime().Unix(),
			string(t.Format()),
			string(t.FileType()),
			checksum,
			t.Title(),
			t.Album(),
			t.Artist(),
			t.AlbumArtist(),
			t.Composer(),
			t.Genre(),
			t.Year(),
			track_num,
			track_total,
			disc_num,
			disc_total,
			hasimage,
			t.Lyrics(),
		)
		if err != nil {
			panic(err)
		}
	}
	return
}
func scanDb() (tracks []*googm.Track, uploadFiles []*os.File) {
	var count int
	if err := DB.QueryRow("select count(*) as total from files where modtime > IFNULL(googmtime,0) LIMIT 10").Scan(&count); err != nil {
		switch {
		case err == sql.ErrNoRows:
			p.INFO.Println("No changes found: error: " + err.Error())
		default:
			p.ERROR.Println(err)
			return
		}
	}
	p.MSG.Println("Starting to load from Db:")
	progress := pb.StartNew(count)
	rows, err := DB.Query("select fullpath from files where modtime > IFNULL(googmtime,0) LIMIT 10")
	if err != nil {
		p.ERROR.Println("select failed; " + err.Error())
		panic(err)
	}
	defer rows.Close()
	var fullpath string
	p.INFO.Printf("%-15v %-35v %-15v %-15v %2v/%-6v\n","ClientId","Title","Album","Artist","##","T#","")
	db_scan_loop:
	for rows.Next(){
		progress.Increment()
		err := rows.Scan(&fullpath)
		if err != nil {
			panic(err)
		}
		fh, err := os.Open(fullpath)
		checksum, err := tag.Sum(fh)
		if err != nil {
			p.ERROR.Println("FAILED CHECKSUM READ: [file skipped] " + err.Error() + fullpath)
			C_INVALID++
			continue db_scan_loop
		}

		t, err := tag.ReadFrom(fh)
		if err != nil {
			p.ERROR.Println("FAILED TAG READ: [file skipped] " + fullpath + " with error: " + err.Error())
			C_INVALID++
			continue db_scan_loop
		}	
		track_num, track_total := t.Track()
		disc_num, disc_total := t.Disc()
		p.INFO.Printf("%-15v %-35v %-15v %-15v %-2v/%-6v",checksum,t.Title(),t.Album(), t.Artist(),track_num, track_total)
		track := &googm.Track{
			ClientId:        checksum,
			Title:           t.Title(),
			Album:           t.Album(),
			Artist:          t.Artist(),
			AlbumArtist:     t.AlbumArtist(),
			Composer:        t.Composer(),
			Year:            t.Year(),
			Genre:           t.Genre(),
			TrackNumber:     track_num,
			TotalTrackCount: track_total,
			DiscNumber:      disc_num,
			TotalDiscCount:  disc_total,
		}
		tracks = append(tracks, track)
		uploadFiles = append(uploadFiles, fh)
		C_TOTAL++
	}
	if err = rows.Err(); err != nil {
		panic(err)
	}
	return
}
func setVerbosity() {
	/**
	* TRACE
	* DEBUG
	* INFO
	* MSG
	* WARN
	* ERROR
	* CRITICAL
	* FATAL
	**/
	if VERBOSE {
		p.INFO.Println("verbose output enabled")
		p.SetLogThreshold(p.LevelTrace)
		p.SetStdoutThreshold(p.LevelTrace)
	} else {
		p.INFO.Println("verbose output disabled")
		p.SetLogThreshold(p.LevelInfo)
		p.SetStdoutThreshold(p.LevelMsg)
	}
}

func shouldFileUpload(fullpath string) bool {
	stmt, err := DB.Prepare("select modtime,googmtime from files where fullpath=?")
	if err != nil {
		p.ERROR.Println("select preparation failed; " + err.Error())
		panic(err)
	}
	var modtime int64
	var googmtime sql.NullInt64
	// only a single row should be returned, unique file
	if err = stmt.QueryRow(fullpath).Scan(&modtime, &googmtime); err != nil {
		switch {
		case err == sql.ErrNoRows:
			p.INFO.Println("New song, row not found for:" + fullpath + " error: " + err.Error())
		default:
			p.ERROR.Println(err)
			return false
		}
	}
	p.TRACE.Printf("%s -> compare modtime: %i to googmtime: %i \n", fullpath, modtime, googmtime)
	if !googmtime.Valid {
		return true // update - never been uploaded to google!
	} else if modtime > googmtime.Int64 {
		return true // this means UPDATE!!, the file has been updated since the last google upload
	}
	return false
}

func validateFile(path string) bool {
	arr := strings.Split(path, ".")
	extension := arr[len(arr)-1]
	for _, test := range MUSIC_EXT {
		if strings.ToLower(test) == strings.ToLower(extension) {
			return true
		}
	}
	return false
}

func openDB() {
	var err error
	DB, err = sql.Open("sqlite3", DBPATH)
	if err != nil {
		p.ERROR.Println("Database failure: " + err.Error())
		panic(err)
	}

}

func loadGoogm() (*googm.Client, error) {
	fh, err := os.Open(OAUTH_CREDENTIAL_FILE)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	if err := json.NewDecoder(fh).Decode(&GOOGM_CREDENTIALS); err != nil {
		return nil, err
	}
	client := OauthConfig.Client(oauth2.NoContext, &GOOGM_CREDENTIALS.Token)
	return googm.NewClient(client, GOOGM_CREDENTIALS.ID)
}

func dispResults() {
	p.INFO.Println("C_TOTAL Files: " + strconv.FormatInt(C_TOTAL, 10))
	p.INFO.Println("Updated Files: " + strconv.FormatInt(C_UPDATED, 10))
	p.INFO.Println("Uploaded Files: " + strconv.FormatInt(CUploaded, 10))
	p.INFO.Println("TOTAL Dirs: " + strconv.FormatInt(C_DIRS, 10))
	p.INFO.Println("Skipped Files: " + strconv.FormatInt(C_SKIPPED, 10))
	p.INFO.Println("Invalid Files: " + strconv.FormatInt(C_INVALID, 10))
}

func register() error {
	var id string = "00:01:02:" + time.Now().Format("15:04:05")
	var name string = VERSION
	url := OauthConfig.AuthCodeURL("", oauth2.AccessTypeOffline)
	p.MSG.Println(`Please open the following URL in a browser to authorize gmusic with your Google account.  Copy the code given to you
at the end of the authorization process below.

%s

> `, url)
	var code string
	if _, err := fmt.Scanln(&code); err != nil {
		return err
	}
	tok, err := OauthConfig.Exchange(oauth2.NoContext, code)
	if err != nil {
		return err
	}
	httpclient := OauthConfig.Client(oauth2.NoContext, tok)
	client, err := googm.NewClient(httpclient, id)
	if err != nil {
		return err
	}
	if err := client.Register(name); err != nil {
		return err
	}
	f, err := os.Create(OAUTH_CREDENTIAL_FILE)
	if err != nil {
		return err
	}
	defer f.Close()
	GOOGM_CREDENTIALS.ID = id
	GOOGM_CREDENTIALS.Token = *tok
	if err := json.NewEncoder(f).Encode(GOOGM_CREDENTIALS); err != nil {
		return err
	}
	p.MSG.Println("registration successful\n")
	return nil
}
