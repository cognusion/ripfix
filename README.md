# ripfix
Bulk PDF image-to-text processor

## Overview

**ripfix** tears apart image-only PDFs using [poppler](https://poppler.freedesktop.org/), and then rebakes them with text using [Tesseract](https://github.com/tesseract-ocr/tesseract). It can handle folders of thousands of PDFs, ripping them apart and stitching them back together, limited only by the CPU you can/want to give to it.

Both [poppler-utils](https://poppler.freedesktop.org/) (specifically **pdftoppm**) and [Tesseract](https://github.com/tesseract-ocr/tesseract) are required to be installed, and in the executable PATH. **ripfix** checks for these and whinges accordingly if they are not easily found.

## Install

Assuming you have a modern [go](https://go.dev/) installed, along with both [poppler-utils](https://poppler.freedesktop.org/) (specifically **pdftoppm**) and [Tesseract](https://github.com/tesseract-ocr/tesseract), it should be this hard:

```
go install github.com/cognusion/ripfix@latest
```

**ripfix** should be platform agnostic, but I have only tested it on Linux. Well-formed issues and PRs are welcome.

## Usage

```bash
Usage of ripfix:
      --clean          Remove temp folders/files when complete. (default true)
  -m, --max int        Maximum number of simultaneous processors. (default 24)
  -o, --out string     Location to place the final products. They will have the same file name as the source. (default "./")
  -p, --pdfs strings   List of PDFs to convert. Globs are fine. Quotes are encouraged.
  -t, --temp string    Location for temp files. (default "/tmp/")
```

### clean

*default: true*

If this is true, temp folders used by each worker will be removed as a worker exits, and the entire **ripfix** temp folder structure will be removed as the program exists.

If this is false, expect a lot of plaque in your *--temp* **ripfix** directory.

### max

*default: [reported number of CPU cores]*

This is how many workers are available to process PDFs. Said differently, this is how many PDFs **ripfix** can process at the same time. The supervisor ensures that as long as there is work to do, this many workers are available: As one exits, another is fired up. Don't worry about this number being higher than the number of PDFs you have to process, as any workers who have nothing to do after all of the work has been assigned will exit.

### out

*default: "./"*

This is where the fixed PDFs will end up.

### pdfs

All the PDFs you want to work on. Globs liked "*.pdf" are valid (note the quotes). They will end up in *--out* named the same with *_fixed* appended. (e.g. *neat.pdf* will be *neat_fixed.pdf*)

### temp

*default: [OS-reported temp location]*

This location will have a folder created called **ripfix**, and in that will be unique directories for each work-worker pair named *pid.sequence_hash*, where *pid* is the process ID number of **ripfix** and *sequence_hash* is a generated ID. Inside each of those folders will be one 300DPI TIFF image per page, and a file named *sequence_hash.lst* which is a list of those TIFF files, for **tesseract** to iterate over.

Assuming you don't disable cleaning, these folders will be cleaned up as each worker exits, and the temporary files are unneeded.
