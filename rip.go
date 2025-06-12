package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cognusion/go-sequence"
	"github.com/cognusion/semaphore"
	"github.com/gofrs/flock"
	"github.com/spf13/pflag"
)

var (
	pdfs         []string
	maxP         int
	out          string
	tmp          string
	tmpFolder    = "ripfix"
	compress     string
	skipExisting bool
	clean        bool
	flockFile    string
	skipFlock    bool
)

// work gets passed around to various funcs/goros.
type work struct {
	id           string
	pdf          string
	tmp          string
	out          string
	compress     string
	skipExisting bool
	clean        bool
}

func init() {
	pflag.StringSliceVarP(&pdfs, "pdfs", "p", make([]string, 0), "List of PDFs to convert. Globs are fine. Quotes are encouraged.")
	pflag.StringVarP(&out, "out", "o", "./", "Location to place the final products. They will have the same file name as the source.")
	pflag.StringVarP(&tmp, "temp", "t", os.TempDir()+"/", "Location for temp files.")
	pflag.IntVarP(&maxP, "max", "m", runtime.NumCPU(), "Maximum number of simultaneous processors.")
	pflag.BoolVar(&clean, "clean", true, "Remove temp folders/files when complete.")
	pflag.StringVarP(&compress, "compress", "c", "none", "Set a compression target to one of 'none' (300DPI), 'ebook' (150DPI), or 'screen' (72DPI).")
	pflag.BoolVar(&skipExisting, "skip", true, "If a suffixed file is encountered, assume it is correct and don't do that part of the process again.")
	pflag.StringVar(&flockFile, "flock", os.TempDir()+"/ripfix.lock", "Location of a file lock file, to ensure two copies of ripfix aren't running at the same time.")
	pflag.BoolVar(&skipFlock, "ignore-flock", false, "DANGER: If true, skips flocking.")

	pflag.CommandLine.MarkHidden("ignore-flock")
	pflag.Parse()

	if len(pdfs) == 0 {
		fmt.Println("ripfix options:")
		pflag.PrintDefaults()
		os.Exit(0)
	}

	// Sanity!
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
		pid      = os.Getpid()
		seq      = sequence.New(1)
		sem      = semaphore.NewSemaphore(maxP)
		workChan = make(chan work)
	)

	// Step -1 Check and set

	// flocking. While not strictly prohibitive if multiple instances of ripfix are running,
	// they *must* all be running --clean=false and that's not the funnest thing to police,
	// so here we are. skipFlock is enabled using a hidden option "ignore-flock".
	if skipFlock {
		fileLock := flock.New(flockFile)
		locked, err := fileLock.TryLock()
		if err != nil {
			panic(fmt.Errorf("error while trying to flock %s: %w", flockFile, err))
		}
		if locked {
			// Bingo!
			defer fileLock.Unlock()
		} else {
			fmt.Println("Only one instance of ripfix should be running at a time.")
			os.Exit(1)
		}
	}

	// Check for pdftoppm, tesseract, and possibly ps2pdf
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		fmt.Println("Could not find path to pdftoppm!")
		os.Exit(1)
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		fmt.Println("Could not find path to tesseract!")
		os.Exit(1)
	}
	if compress != "none" {
		if _, err := exec.LookPath("ps2pdf"); err != nil {
			fmt.Println("Could not find path to ps2pdf!")
			os.Exit(1)
		}
	}

	// Confirm out is a folder
	if s, serr := os.Stat(out); serr != nil {
		panic(serr)
	} else if !s.IsDir() {
		fmt.Printf("Output location '%s' is not a directory.\n", out)
		os.Exit(1)
	}

	// Ensure the base tmp folder is available
	if terr := os.MkdirAll(tmp+tmpFolder, os.ModePerm); terr != nil {
		panic(terr)
	}
	if clean {
		defer os.RemoveAll(tmp + tmpFolder)
	}

	// Step 0 workers
	// We are ok with starting too many workers, as any unneeded will just exit
	// after the work is assigned.
	progressChan, doneChan := supervisor(&sem, workChan)
	defer close(progressChan)

	// read from progressChan and print stuff
	go func() {
		for p := range progressChan {
			switch v := p.(type) {
			case error:
				fmt.Printf("[PROGRESS] ERROR: %s\n", v)
			case string:
				fmt.Printf("[PROGRESS] %s\n", v)
			default:
				fmt.Printf("[PROGRESS] ??: %+v\n", v)
			}
		}
	}()

	// Step 1 build work and dole it out
	for _, file := range buildList(pdfs) {
		id := seq.NextHashID()
		//fmt.Printf("[WORKFILE] %s is %s\n", file, id)
		workChan <- work{
			id:           id,
			pdf:          file,
			tmp:          fmt.Sprintf("%s%s/%d.%s/", tmp, tmpFolder, pid, id),
			out:          out,
			compress:     compress,
			skipExisting: skipExisting,
			clean:        clean,
		}
	}
	// POST: each work{} has been consumed by a worker.

	// Signal we're done, so any idle workers can exit
	close(doneChan)

	// wait until all of the workers are done
	<-sem.IsFree(100 * time.Millisecond)
}

// supervisor takes a Semaphore and a channel where work will be assigned, immediately returning two channels:
//
//	The first channel (progressChan) is where progress updates (string) an errors (error) are sent. If an error is sent, the worker will exit.
//	The second channel (doneChan) is used to signal the supervisor and workers when there will be no more work assigned, via closing.
//
// The supervisor ensures that there are always 'maxP' workers waiting (using the Semaphore), listening for work, until doneChan is closed.
// Each worker does *at most one* item of work, exiting when it is complete. The supervisor will start a new worker if it is still employed.
func supervisor(lock *semaphore.Semaphore, workChan chan work) (progressChan chan any, doneChan chan struct{}) {
	doneChan = make(chan struct{})
	progressChan = make(chan any)

	go func() {
		// TODO: Get rid of this incrementer. But good for debugging.
		c := 0
		for {
			c++
			select {
			case <-lock.Until():
				// woo! make a worker!
				go func(i int) {
					progressChan <- fmt.Sprintf("[WORKER %d] Start", i)
					defer lock.Unlock()
					defer func() { progressChan <- fmt.Sprintf("[WORKER %d] Done", i) }()

					select {
					case w := <-workChan:
						var err error
						// Step 2 get work
						progressChan <- fmt.Sprintf("[WORKER %d] Work! %+v", i, w)

						// Step 3a ensure path
						err = os.MkdirAll(w.tmp, os.ModePerm)
						if err != nil {
							progressChan <- fmt.Errorf("[WORKER %d] Error MkdirAll '%s': %w", i, w.tmp, err)
							return
						}
						if w.clean {
							// These temp folder full of TIFFs can get massive.
							// Clean up.
							defer os.RemoveAll(w.tmp)
						}

						// Step 3b craft the future file names, and see if the compressed product exists for an early exit.
						outFile := w.out + strings.TrimSuffix(filepath.Base(w.pdf), filepath.Ext(filepath.Base(w.pdf))) + "_fixed" // tesseract wants an extensionless filename
						compressFile := fmt.Sprintf("%s_%s.pdf", outFile, w.compress)
						if w.compress != "none" && w.skipExisting && fileExists(compressFile) {
							progressChan <- fmt.Sprintf("[WORKER %d] Compress file '%s' already exists. Skipping all the things!", i, compressFile)
							return
						}

						// Step 4 rip PDFs into TIFFs
						// Step 5 OCR the TIFFs and fix them into PDFs again
						if !(w.skipExisting && fileExists(outFile+".pdf")) {
							progressChan <- fmt.Sprintf("[WORKER %d] pdfToTiff(%s, %s)", i, w.pdf, w.tmp)

							err = w.ripfix(i, progressChan)
							if err != nil {
								progressChan <- fmt.Errorf("[WORKER %d] Error: %w", i, err)
								return
							}
						} else {
							progressChan <- fmt.Sprintf("[WORKER %d] %s.pdf found, skipping pdfToTiff and tesseract", i, outFile)
						}
						productFile := outFile + ".pdf"

						// Step 6 maybe compress the images in the PDF
						if w.compress != "none" {
							nOutFile := outFile + ".pdf"
							if !(w.skipExisting && fileExists(compressFile)) {
								progressChan <- fmt.Sprintf("[WORKER %d] compressPdf(%s, %s, %s)", i, w.compress, nOutFile, compressFile)
								err = compressPdf(w.compress, nOutFile, compressFile)
								if err != nil {
									progressChan <- fmt.Errorf("[WORKER %d] Error compressPdf '%s' '%s' -> '%s': %w", i, w.compress, nOutFile, compressFile, err)
									return
								}
							} else {
								progressChan <- fmt.Sprintf("[WORKER %d] %s found, skipping compressPdf", i, compressFile)
							}
							productFile = compressFile // update
							if w.clean {
								// I'm conflicted about this, as it took a lot of work to make that file, and if we don't like the compressed version,
								// we may want to recompress it using a different setting "manually", but also understand why we're doing this, as 1G PDFs
								// are better as 200MB PDFs, not 1200MBs of PDFs :)
								defer os.Remove(nOutFile)
							}
						}

						// Step N celebrate!
						progressChan <- fmt.Sprintf("[WORKER %d] Completed Work! See '%s'", i, productFile)
					case <-doneChan:
						// That's all folks!
						return
					}
				}(c)
			case <-doneChan:
				// That's all folks!
				return
			}
		}
	}()

	return // progressChan, doneChan
}

// buildList will possibly recursively (if a glob is provided) create a list of files to assign as work.
func buildList(files []string) []string {
	l := make([]string, 0)
	for _, file := range files {
		//fmt.Printf("[FILE] %s\n", file)
		if strings.Contains(file, "*") || strings.Contains(file, "?") {
			gfiles, err := filepath.Glob(file)
			if err != nil {
				panic(err)
			}
			l = append(l, buildList(gfiles)...) // recursion
		} else if s, err := os.Stat(file); err != nil {
			// We we can't stat the thing, something is very wrong.
			panic(fmt.Errorf("file %s cannot be found: %w", file, err))
		} else if !s.IsDir() {
			// file
			l = append(l, file)
		}
	}
	return l
}

// fileExists returns true if a file exists, and false if it doesn't.
func fileExists(file string) bool {
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return true
}

// simplerun is an abstraction to execute a Command.
func simpleRun(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

// ripfix abstracts some clumsy code to get it out of the supervisor, and attach it to the work.
// The next generation will put all of the code on the work so the supervisor can just handle errors and keep the workers working.
func (w *work) ripfix(i int, progressChan chan any) error {
	var (
		err     error
		outFile = w.out + strings.TrimSuffix(filepath.Base(w.pdf), filepath.Ext(filepath.Base(w.pdf))) + "_fixed"
	)

	// Step 4a rip the PDF into TIFFs
	err = pdfToTiff(w.pdf, w.tmp)
	if err != nil {
		return fmt.Errorf("pdftoppm '%s' -> '%s': %w", w.pdf, w.tmp, err)
	}
	// Step 4b create list of result files, w.tmp+w.id+".lst"
	progressChan <- fmt.Sprintf("[WORKER %d] createTiffList", i)

	listFile, lErr := w.createTiffList()
	if lErr != nil {
		return fmt.Errorf("createTiffList: %w", lErr)
	}

	// Step 5 tesseract the TIFFs
	progressChan <- fmt.Sprintf("[WORKER %d] tesseract(%s, %s)", i, listFile, outFile)
	err = tesseract(listFile, outFile)
	if err != nil {
		return fmt.Errorf("tesseract '%s' -> '%s': %w", w.tmp, outFile, err)
	}

	return nil
}

// createTiffList assembles a list of -presumably- the TIFF images created by pdfToTiff,
// writing it to a file that tesseract can read.
func (w *work) createTiffList() (string, error) {
	var (
		gfiles []string
		f      *os.File
		err    error
	)
	listFile := fmt.Sprintf("%s%s.lst", w.tmp, w.id)
	gfiles, err = filepath.Glob(w.tmp + "*.tif")
	if err != nil {
		return "", fmt.Errorf("error getting tiffs '%s': %w", w.tmp, err)
	}
	f, err = os.Create(listFile)
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
