# ripfix
Bulk PDF image-to-text processor

## Overview

**ripfix** tears apart image-only PDFs using [poppler](https://poppler.freedesktop.org/), and then rebakes them with text using [Tesseract](https://github.com/tesseract-ocr/tesseract). It can handle folders of thousands of PDFs, ripping them apart and stitching them back together, limited only by the CPU you can/want to give to it.

Both [poppler-utils](https://poppler.freedesktop.org/) (specifically **pdftoppm**) and [Tesseract](https://github.com/tesseract-ocr/tesseract) are required to be installed, and in the executable PATH. If you want to compress PDFs then [GhostScript](https://www.ghostscript.com/releases/gsdnld.html) (specifically **ps2pdf**) is also required. **ripfix** checks for these and whinges accordingly if they are not easily found.

## Install

Assuming you have a modern [go](https://go.dev/) installed, along with [poppler-utils](https://poppler.freedesktop.org/) (specifically **pdftoppm**), [Tesseract](https://github.com/tesseract-ocr/tesseract), and optionally [GhostScript](https://www.ghostscript.com/releases/gsdnld.html) (specifically **ps2pdf**), it should be this hard:

```
go install github.com/cognusion/ripfix@latest
```

**ripfix** should be platform agnostic, but I have only tested it on Linux. Well-formed issues and PRs are welcome.

## Usage

```bash
Usage of ripfix:
  -b, --bar               Enable progress bar, suppress normal non-error screen logging.
      --clean             Remove temp folders/files when complete. (default true)
  -c, --compress string   Set a compression target to one of 'none' (300DPI), 'ebook' (150DPI), or 'screen' (72DPI). (default "none")
      --flock string      Location of a file lock file, to ensure two copies of ripfix aren't running at the same time. (default "/tmp/ripfix.lock")
  -l, --log string        If set, normal screen logging will go to the file instead, including when used with --bar.
  -m, --max int           Maximum number of simultaneous processors. (default 12)
  -o, --out string        Location to place the final products. They will have the same file name as the source. (default "./")
  -p, --pdfs strings      List of PDFs to convert. Globs are fine. Quotes are encouraged.
      --reprocess         ONLY reprocess PDFs that have existing suffixes. Disables 'skip'. Use with care.
      --skip              If a suffixed file is encountered, assume it is correct and don't do that part of the process again. (default true)
  -t, --temp string       Location for temp files. (default "/tmp/")
```
### bar

*default: false*

If true, suppress non-error output and toss up a progress bar, ticking away as each file stage is complete.

### clean

*default: true*

If this is true, temp folders used by each worker will be removed as a worker exits, and the entire **ripfix** temp folder structure will be removed as the program exists.

If this is false, expect a lot of plaque in your *--temp* **ripfix** directory.

### compress

*default: none*

If this value not *none*, after **ripfix** generates a *_fixed* PDF, it will run **ps2pdf** with this value as the target style. The TIFFs are ripped out at 300DPI, and reassembled at the same, so values at/above that are pointless leaving *"ebook"* (150DPI-ish) and *"screen"* (72DPI-ish).

Of note, if *clean* is true, the *_fixed* PDF will be removed after the compressed version of *_fixed_[style]* is finished.

### flock

*default: [OS-reported temp location]/ripfix.lock*

Location of a file that will be locked when an instance of **ripfix** is running. If another is started up it will be unable to lock the file and return an appropriate message whilst exiting.

While not strictly prohibitive if multiple instances of ripfix are running, they *must* all be running *clean==false* or they will clobber each other on the way out. This solves that.

### log

Location of a file to send the normal screen logging output to. The file will be appended to if it exists. The file will be created if it doesn't. The folder path should exist. If specified, this log will be written to even if --bar is used.

### max

*default: [reported number of CPU cores]*

This is how many workers are available to process PDFs. Said differently, this is how many PDFs **ripfix** can process at the same time. The supervisor ensures that as long as there is work to do, this many workers are available: As one exits, another is fired up. Don't worry about this number being higher than the number of PDFs you have to process, as any workers who have nothing to do after all of the work has been assigned will exit.

### out

*default: "./"*

This is where the fixed PDFs will end up.

### pdfs

All the PDFs you want to work on. Globs liked "*.pdf" are valid (note the quotes). They will end up in *--out* named the same with *_fixed* appended. (e.g. *neat.pdf* will be *neat_fixed.pdf*)

### skip

*default: true*

Depending on your options, there are up to two resulting products: a *_fixed.pdf* and a *_fixed_[compress style].pdf*. If *skip* is true, and one of these is encountered, then the phases that generated that product are skipped. Either delete the products or disable skip.

Of note, if *clean* was true when generating a *compress*ed product, the intermediary *_fixed.pdf* will have been deleted, thus it will be re-generated.

### temp

*default: [OS-reported temp location]*

This location will have a folder created called **ripfix**, and in that will be unique directories for each work-worker pair named *pid.sequence_hash*, where *pid* is the process ID number of **ripfix** and *sequence_hash* is a generated ID. Inside each of those folders will be one 300DPI TIFF image per page, and a file named *sequence_hash.lst* which is a list of those TIFF files, for **tesseract** to iterate over.

Assuming you don't disable cleaning, these folders will be cleaned up as each worker exits, and the temporary files are unneeded.
