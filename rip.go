package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/cognusion/go-racket"
	"github.com/cognusion/go-sequence"
	"github.com/gofrs/flock"
	"github.com/spf13/pflag"
)

const (
	suffixFixed string = "_fixed"
)

var (
	pdfs         []string
	maxP         int
	out          string
	tmp          string
	tmpFolder    = "ripfix"
	compress     string
	skipExisting bool
	reprocess    bool
	clean        bool
	flockFile    string
	skipFlock    bool
	useBar       bool
	logFile      string
	debug        bool
	dupes        bool
	dupeMap      sync.Map
)

func init() {
	pflag.StringSliceVarP(&pdfs, "pdfs", "p", make([]string, 0), "List of PDFs to convert. Globs are fine. Quotes are encouraged.")
	pflag.StringVarP(&out, "out", "o", "./", "Location to place the final products. They will have the same file name as the source.")
	pflag.StringVarP(&tmp, "temp", "t", os.TempDir()+"/", "Location for temp files.")
	pflag.IntVarP(&maxP, "max", "m", runtime.NumCPU(), "Maximum number of simultaneous processors.")
	pflag.BoolVar(&clean, "clean", true, "Remove temp folders/files when complete.")
	pflag.StringVarP(&compress, "compress", "c", "none", "Set a compression target to one of 'none' (300DPI), 'ebook' (150DPI), or 'screen' (72DPI).")
	pflag.BoolVar(&skipExisting, "skip", true, "If a suffixed file is encountered, assume it is correct and don't do that part of the process again.")
	pflag.BoolVar(&reprocess, "reprocess", false, "ONLY reprocess PDFs that have existing suffixes. Disables 'skip'. Use with care.")
	pflag.StringVar(&flockFile, "flock", os.TempDir()+"/ripfix.lock", "Location of a file lock file, to ensure two copies of ripfix aren't running at the same time.")
	pflag.BoolVar(&skipFlock, "ignore-flock", false, "DANGER: If true, skips flocking.")
	pflag.BoolVarP(&useBar, "bar", "b", false, "Enable progress bar, suppress normal non-error screen logging.")
	pflag.StringVarP(&logFile, "log", "l", "", "If set, normal screen logging will go to the file instead, including when used with --bar.")
	pflag.BoolVar(&debug, "debug", false, "Enables debug logging. Disables bar.")
	pflag.BoolVar(&dupes, "dupes", false, "Enables deduplication. Every file processed gets a sha256 hash, and if a dupe is found the subsequents are skipped.")

	pflag.CommandLine.MarkHidden("ignore-flock")
	pflag.Parse()

	if len(pdfs) == 0 {
		fmt.Println("ripfix options:")
		pflag.PrintDefaults()
		os.Exit(0)
	}

	// Sanity!
	if debug {
		useBar = false
	}
	if reprocess {
		// reprocess overrides the skip
		skipExisting = false
	}
	if maxP < 1 {
		// We need at least one processor, or deadlock
		maxP = 1
	}
	if !strings.HasSuffix(tmp, "/") {
		tmp += "/"
	}
	if !strings.HasSuffix(out, "/") {
		out += "/"
	}
	if !(compress == "none" || compress == "ebook" || compress == "screen") {
		fmt.Printf("Compress option invalid: %s\n", compress)
		pflag.PrintDefaults()
		os.Exit(1)
	}
}

func main() {

	var (
		pid         = os.Getpid()
		seq         = sequence.New(1)
		workChan    = make(chan racket.Work)
		fileLock    *flock.Flock
		logMessages = true
		outLog      = log.New(os.Stderr, "", log.LstdFlags)
		debugLog    = log.New(io.Discard, "", 0)
		barTmpl     = `{{ counters . }} {{ bar . }} {{ percent . }}`
		barChan     chan racket.Progress
	)

	if debug {
		debugLog = log.New(os.Stderr, "[DEBUG] ", log.Lshortfile)
	}

	if useBar {
		barChan = make(chan racket.Progress)
		defer close(barChan)
		logMessages = false // else gross

		go func() {
			bar := pb.ProgressBarTemplate(barTmpl).Start(len(pdfs))
			// bar.Set(pb.Bytes, true)
			defer bar.Finish()

			for b := range barChan {
				switch b.Type {
				case racket.ProgressUpdate:
					bar.Add64(b.Data.(int64))
				case racket.ProgressEstimate:
					bar.SetTotal(b.Data.(int64))
				}
			}
		}()
		time.Sleep(100 * time.Millisecond)
	}

	// Step -1 Check and set

	// flocking. While not strictly prohibitive if multiple instances of ripfix are running,
	// they *must* all be running --clean=false and that's not the funnest thing to police,
	// so here we are. skipFlock is enabled using a hidden option "ignore-flock".
	if !skipFlock {
		fileLock = flock.New(flockFile)
		locked, err := fileLock.TryLock()
		if err != nil {
			panic(fmt.Errorf("error while trying to flock %s: %w", flockFile, err))
		}
		if locked {
			// Bingo!
			defer fileLock.Unlock()
		} else {
			die("Only one instance of ripfix should be running at a time.\n")
		}
	}

	// If we're logging to a file, check it out here.
	if logFile != "" {
		logMessages = true // in case of --bar, it is set false
		f, err := os.OpenFile(path.Clean(logFile), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
		if err != nil {
			die("Could not open logfile '%s' for append: %s\n", logFile, err)
		}
		outLog = log.New(f, "", log.LstdFlags)
	}

	// Check for pdftoppm, tesseract, and possibly ps2pdf
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		die("Could not find path to pdftoppm!\n")
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		die("Could not find path to tesseract!\n")
	}
	if compress != "none" {
		if _, err := exec.LookPath("ps2pdf"); err != nil {
			die("Could not find path to ps2pdf!\n")
		}
	}

	// Confirm out is a folder
	if s, serr := os.Stat(out); serr != nil {
		panic(serr)
	} else if !s.IsDir() {
		die("Output location '%s' is not a directory.\n", out)
	}

	// Ensure the base tmp folder is available
	if terr := os.MkdirAll(tmp+tmpFolder, 0750); terr != nil {
		panic(terr)
	}
	if clean {
		defer os.RemoveAll(tmp + tmpFolder)
	}

	// Oy! No printing other than to logs from this point!
	debugLog.Printf("RipFix job starting...\n")

	// Step 0 workers
	// We are ok with starting too many workers, as any unneeded will just exit
	// after the work is assigned.
	rfJob := racket.NewJob(ripFixWorkFunc)
	progressChan, doneFunc := rfJob.Supervisor(maxP, workChan)
	defer close(progressChan)

	debugLog.Printf("\tSupervisor running...\n")

	go racket.ProgressLogger(outLog, logMessages, nil, progressChan, barChan)

	debugLog.Printf("\tProcessLogger running...\n")

	// Step 1 build work and dole it out
	for _, file := range buildList(pdfs, barChan, progressChan) {
		id := seq.NextHashID()
		//outLog.Printf("[WORKFILE] %s is %s\n", file, id)
		workChan <- racket.NewWork(map[string]any{
			"id":           id,
			"pdf":          file,
			"temp":         fmt.Sprintf("%s%s/%d.%s/", tmp, tmpFolder, pid, id),
			"out":          out,
			"compress":     compress,
			"skipExisting": skipExisting,
			"reprocess":    reprocess,
			"clean":        clean,
			"dupes":        dupes,
		})
	}
	// POST: each work{} has been consumed by a worker.

	// Signal we're done, so any idle workers can exit
	doneFunc()
	debugLog.Printf("\tDone sending work. Waiting...\n")

	// wait until all of the workers are done
	<-rfJob.IsDone()
	debugLog.Printf("\tJob is done!\n")
}

// ripFixWorkFunc is a racket.WorkerFunc that will get handed Work by the Supervisor.
func ripFixWorkFunc(id any, w racket.Work, progressChan chan<- racket.Progress) {
	// Got work
	var (
		err         error
		productFile string
	)

	progressChan <- racket.PMessagef("[WORKER %v] Work! %+v", id, w)

	if w.GetBool("dupes") {
		defer resolveDupes(id, w.GetString("pdf"), &productFile, progressChan)
	}

	// Ensure path
	err = os.MkdirAll(w.GetString("temp"), 0750)
	if err != nil {
		progressChan <- racket.PErrorf("[WORKER %v] Error MkdirAll '%s': %w", id, w.GetString("temp"), err)
		return
	}
	if w.GetBool("clean") {
		// These temp folder full of TIFFs can get massive.
		// Clean up.
		defer os.RemoveAll(w.GetString("temp"))
	}

	// Craft the future file names, and see if the compressed product exists for an early exit.
	outFile := fmt.Sprintf("%s%s%s", w.GetString("out"), strings.TrimSuffix(filepath.Base(w.GetString("pdf")), filepath.Ext(filepath.Base(w.GetString("pdf")))), suffixFixed) // tesseract wants an extensionless filename
	compressFile := fmt.Sprintf("%s_%s.pdf", outFile, w.GetString("compress"))

	// Set productFile, so we know what the result will be early.
	if w.GetString("compress") != "none" {
		productFile = compressFile
	} else {
		productFile = outFile + ".pdf"
	}

	if w.GetBool("reprocess") && !(fileExists(outFile) || fileExists(compressFile)) {
		// There is no need to process this file, as there is not a fixed variant existing.
		progressChan <- racket.PMessagef("[WORKER %v] Reprocessing '%s' unneeded, as no fixed variant exists. Completed Work! Skipping all the things!", id, w.GetString("pdf"))
		progressChan <- racket.PUpdate(1)
		return
	}

	if w.GetString("compress") != "none" && w.GetBool("skipExisting") && fileExists(compressFile) {
		// There is no need to craft _fixed if _fixed_[compress] exists.
		progressChan <- racket.PMessagef("[WORKER %v] Compress file '%s' already exists. Completed Work! Skipping all the things!", id, compressFile)
		progressChan <- racket.PUpdate(1)
		return
	}

	// Rip PDFs into TIFFs,
	// OCR the TIFFs,
	// and fix them into PDFs again
	if !(w.GetBool("skipExisting") && fileExists(outFile+".pdf")) {
		progressChan <- racket.PMessagef("[WORKER %v] pdfToTiff(%s, %s)", id, w.GetString("pdf"), w.GetString("temp"))

		err = ripfix(id, w, progressChan)
		if err != nil {
			progressChan <- racket.PErrorf("[WORKER %v] Error: %w", id, err)
			return
		}
	} else {
		progressChan <- racket.PMessagef("[WORKER %v] %s.pdf found, skipping pdfToTiff and tesseract", id, outFile)
	}

	// Maybe compress the images in the PDF
	if w.GetString("compress") != "none" {
		nOutFile := outFile + ".pdf"
		if !(w.GetBool("skipExisting") && fileExists(compressFile)) {
			progressChan <- racket.PMessagef("[WORKER %v] compressPdf(%s, %s, %s)", id, w.GetString("compress"), nOutFile, compressFile)
			err = compressPdf(w.GetString("compress"), nOutFile, compressFile)
			if err != nil {
				progressChan <- racket.PErrorf("[WORKER %v] Error compressPdf '%s' '%s' -> '%s': %w", id, w.GetString("compress"), nOutFile, compressFile, err)
				return
			}
		} else {
			progressChan <- racket.PMessagef("[WORKER %v] %s found, skipping compressPdf", id, compressFile)
		}
		if w.GetBool("clean") {
			// We are conflicted about this, as it took a lot of work to make that file, and if we don't like the compressed version,
			// we may want to recompress it using a different setting "manually", but also understand why we're doing this, as 1G PDFs
			// are better as 200MB PDFs, not 1200MBs of PDFs :)
			defer os.Remove(nOutFile)
		}
	}

	// Step N celebrate!
	progressChan <- racket.PMessagef("[WORKER %v] Completed Work! See '%s'", id, productFile)
	progressChan <- racket.PUpdate(1)
}

// resolveDupes goes through the tedium of determining what -if any- copies need to occur because of duplicate files.
func resolveDupes(id any, basePDF string, productFile *string, progressChan chan<- racket.Progress) {
	// Dupe detection!
	h, e := calculateSHA256Sum(basePDF)
	if e != nil {
		panic(e)
	}

	base := strings.TrimSuffix(basePDF, filepath.Ext(basePDF))

	v, ok := dupeMap.Load(h)
	if ok {
		// There are dupes!
		var ns []string
		switch t := v.(type) {
		case string:
			// one
			ns = []string{t}
		case []string:
			// more
			ns = t
		}
		for _, f := range ns {
			// copies!
			if f == basePDF {
				continue
			}
			baseF := strings.TrimSuffix(f, filepath.Ext(f))
			pf := strings.Replace(*productFile, base, baseF, 1)
			if skipExisting && fileExists(pf) {
				// exists, and we are skipping, so skip it.
				progressChan <- racket.PMessagef("[WORKER %v] Post-process dupe copy of '%s' to '%s' for %s' skipped, as it exists!", id, *productFile, pf, f)
				continue
			}
			progressChan <- racket.PMessagef("[WORKER %v] Post-process dupe copy of '%s' to '%s' for %s'", id, *productFile, pf, f)
			_, e := copyFile(*productFile, pf)
			if e != nil {
				panic(e)
			}
		}

	}
}

// ripfix is an abstraction to get these steps out of ripFixWorkFunc so it is easier to skip them if needed.
func ripfix(workerID any, w racket.Work, progressChan chan<- racket.Progress) error {
	var (
		err     error
		outFile = fmt.Sprintf("%s%s%s", w.GetString("out"), strings.TrimSuffix(filepath.Base(w.GetString("pdf")), filepath.Ext(filepath.Base(w.GetString("pdf")))), suffixFixed) // tesseract wants an extensionless filename
	)

	// Step 4a rip the PDF into TIFFs
	err = pdfToTiff(w.GetString("pdf"), w.GetString("temp"))
	if err != nil {
		return fmt.Errorf("pdftoppm '%s' -> '%s': %w", w.GetString("pdf"), w.GetString("temp"), err)
	}
	// Step 4b create list of result files, w.GetString("temp")+w.GetString("id")+".lst"
	progressChan <- racket.PMessagef("[WORKER %v] createTiffList", workerID)

	listFile, lErr := createTiffList(w)
	if lErr != nil {
		return fmt.Errorf("createTiffList: %w", lErr)
	}

	// Step 5 tesseract the TIFFs
	progressChan <- racket.PMessagef("[WORKER %v] tesseract(%s, %s)", workerID, listFile, outFile)
	err = tesseract(listFile, outFile)
	if err != nil {
		return fmt.Errorf("tesseract '%s' -> '%s': %w", w.GetString("temp"), outFile, err)
	}

	return nil
}

// createTiffList assembles a list of -presumably- the TIFF images created by pdfToTiff,
// writing it to a file that tesseract can read.
func createTiffList(w racket.Work) (string, error) {
	var (
		gfiles []string
		f      *os.File
		err    error
	)
	listFile := fmt.Sprintf("%s%s.lst", w.GetString("temp"), w.GetString("id"))
	gfiles, err = filepath.Glob(w.GetString("temp") + "*.tif")
	if err != nil {
		return "", fmt.Errorf("error getting tiffs '%s': %w", w.GetString("temp"), err)
	}
	f, err = os.Create(path.Clean(listFile))
	if err != nil {
		return "", fmt.Errorf("error creating list file '%s': %w", listFile, err)
	}
	defer f.Close()
	for _, line := range gfiles {
		if _, werr := f.WriteString(line + "\n"); werr != nil {
			return "", fmt.Errorf("error writing to '%s': %w", listFile, err)
		}
	}
	return listFile, nil
}

// buildList will possibly recursively (if a glob is provided) create a list of files to assign as work.
func buildList(files []string, count chan racket.Progress, progressChan chan<- racket.Progress) []string {
	l := make([]string, 0)
	for _, file := range files {
		//fmt.Printf("[FILE] %s\n", file)
		if strings.Contains(file, suffixFixed) {
			// we don't want to process the output of previous processes!
			continue
		}
		if strings.Contains(file, "*") || strings.Contains(file, "?") {
			gfiles, err := filepath.Glob(file)
			if err != nil {
				panic(err)
			}
			l = append(l, buildList(gfiles, nil, progressChan)...) // recursion, but don't send the chan!
		} else if s, err := os.Stat(file); err != nil {
			// We we can't stat the thing, something is very wrong.
			panic(fmt.Errorf("file %s cannot be found: %w", file, err))
		} else if !s.IsDir() {
			// file
			if dupes {
				h, e := calculateSHA256Sum(file)
				if e != nil {
					panic(e)
				}
				v, loaded := dupeMap.LoadOrStore(h, file)
				if loaded {
					// DUPE! We aren't handling those yet
					progressChan <- racket.PMessagef("[BUILDLIST] DUPE! File '%+v' and '%s' share a sum (%s)!", v, file, h)
					var ns []string
					switch t := v.(type) {
					case string:
						ns = []string{
							t,
							file,
						}
					case []string:
						ns = append(t, file)
					}
					dupeMap.Store(h, ns)
					continue
				}
			}
			l = append(l, file)
		}
	}
	if count != nil {
		count <- racket.PEstimate(int64(len(l)))
	}
	return l
}

// calculateSHA256Sum calculates the SHA-256 checksum of a file.
func calculateSHA256Sum(filePath string) (string, error) {
	//#nosec G304 -- Yes, but no.
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close() // Ensure the file is closed when the function exits

	hash := sha256.New() // Create a new SHA-256 hash function

	// Copy the file's content into the hash function
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to copy file content to hash: %w", err)
	}

	// Get the final hash sum and encode it to a hexadecimal string
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// copyFile ... copies a file.
func copyFile(src, dst string) (int64, error) {
	//#nosec G304 - Open the source file for reading
	sourceFile, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer sourceFile.Close() // Ensure the source file is closed

	// Get file info to preserve permissions
	sourceFileInfo, err := sourceFile.Stat()
	if err != nil {
		return 0, err
	}

	//#nosec G304 - Create the destination file with the same permissions as the source
	destinationFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, sourceFileInfo.Mode())
	if err != nil {
		return 0, err
	}
	defer destinationFile.Close() // Ensure the destination file is closed

	// Copy the contents from source to destination
	bytesCopied, err := io.Copy(destinationFile, sourceFile)
	if err != nil {
		return 0, err
	}

	return bytesCopied, nil
}

// pdfToTiff constructs a pdftoppm Command to extract PDF pages as TIFF images.
func pdfToTiff(pdf string, outFolder string) error {
	return simpleRun("pdftoppm", "-tiff", "-r", "300", pdf, outFolder+"page")
}

// tesseract constructs a tesseract Command to do OCR on the TIFF images and reassemble them as a PDF.
func tesseract(fileList, outpath string) error {
	return simpleRun("tesseract", fileList, outpath, "pdf")
}

// compress again pulls apart the PDF, an compresses the PDF using ps2pdf
func compressPdf(style, pdfin, pdfout string) error {
	return simpleRun("ps2pdf", "-dPDFSETTINGS=/"+style, pdfin, pdfout)
}
