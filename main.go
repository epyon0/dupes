package main

import (
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"golang.org/x/term"

	lib "github.com/epyon0/epyonLib"
)

var procTime bool
var quiet bool
var verbose bool
var vverbose bool
var vvverbose bool
var outFile string
var dirs []string

var sem = make(chan struct{}, runtime.GOMAXPROCS(0))
var (
	filesBySize      = make(map[int64][]string)
	filesBySizeMutex sync.RWMutex
)
var (
	filesByCrc      = make(map[uint32][]string)
	filesByCrcMutex sync.RWMutex
)

func walkDir(path string, wg *sync.WaitGroup) {
	defer wg.Done()
	sem <- struct{}{}
	defer func() { <-sem }()

	lib.Verbose(fmt.Sprintf("RUNNING GOROUTINES: %d", runtime.NumGoroutine()), vvverbose)

	info, err := os.Stat(path)
	if err != nil {
		lib.Verbose(fmt.Sprintf("error getting info for %s: %v\n", path, err), vvverbose)
		return
	}

	fileMode := info.Mode()

	switch {
	case fileMode.IsDir():
		lib.Verbose(fmt.Sprintf("Object is DIRECTORY: %s", path), vvverbose)
		entries, err := os.ReadDir(path)
		if err != nil {
			lib.Verbose(fmt.Sprintf("error directroy [%s] (%v)", path, err), vvverbose)
		}
		for _, entry := range entries {
			wg.Add(1)
			go walkDir(filepath.Join(path, entry.Name()), wg)
		}
	case fileMode.IsRegular():
		lib.Verbose(fmt.Sprintf("Object is FILE: %s    SIZE: %s  [%d B]", path, lib.HumanizeBytes(info.Size(), false), info.Size()), vvverbose)
		if verbose && term.IsTerminal(int(os.Stdout.Fd())) {
			width, _, err := term.GetSize(int(os.Stdout.Fd()))
			lib.Er(err)
			fmt.Fprintf(os.Stdout, "\033[0G\033[0K%s", TruncString(path, width))
		}
		filesBySizeMutex.Lock()
		if _, exists := filesBySize[info.Size()]; exists {
			filesBySize[info.Size()] = append(filesBySize[info.Size()], path)
		} else {
			filesBySize[info.Size()] = []string{path}
		}
		filesBySizeMutex.Unlock()
	case fileMode&os.ModeNamedPipe != 0:
		lib.Verbose(fmt.Sprintf("Object is NAMED PIPE: %s", path), vvverbose)
	case fileMode&os.ModeSocket != 0:
		lib.Verbose(fmt.Sprintf("Object is SOCKET: %s", path), vvverbose)
	case fileMode&os.ModeSymlink != 0:
		lib.Verbose(fmt.Sprintf("Object is SYMBOLIC LINK: %s", path), vvverbose)
	default:
		lib.Verbose(fmt.Sprintf("Object is UNKNOWN: %s", path), vvverbose)
	}
	if verbose {
		fmt.Fprintf(os.Stdout, "\033[2K\033[0G")
	}
}

func main() {
	var args []string = os.Args[1:]

	for i := 0; i < len(args); i++ {
		var arg string = args[i]

		if arg == "-h" || arg == "--help" {
			fmt.Fprintf(os.Stdout, "\n\n%s -- Find all duplicate files in given directory or directories.\n\n", os.Args[0])
			fmt.Fprintf(os.Stdout, "  -h | --help                     Print this help message\n")
			fmt.Fprintf(os.Stdout, "  -v | --verbose                  Verbose output (slower)\n")
			fmt.Fprintf(os.Stdout, " -vv | --vverbose                 Very verbose output (even slower)\n")
			fmt.Fprintf(os.Stdout, "-vvv | --vvverbose                Very very verbose output (even slower still)\n")
			fmt.Fprintf(os.Stdout, "  -q | --quiet                    Supress output\n")
			fmt.Fprintf(os.Stdout, "  -t | --time                     Show processing time\n")
			fmt.Fprintf(os.Stdout, " (-d | --dir) <DIR1> <DIR2> ...   Directories to read from\n")
			fmt.Fprintf(os.Stdout, " (-o | --output) <FILE.CSV>       Output data to CSV file\n")

			fmt.Fprint(os.Stdout, "\n")
			os.Exit(0)
		}

		if arg == "-v" || arg == "--verbose" {
			verbose = true
			procTime = true
		}

		if arg == "-vv" || arg == "--vvverbose" {
			verbose = true
			vverbose = true
			procTime = true
			lib.Verbose("Very verbose enabled", vverbose)
		}

		if arg == "-vvv" || arg == "--vvverbose" {
			verbose = true
			vverbose = true
			vvverbose = true
			procTime = true
			lib.Verbose("Very very verbose enabled", vvverbose)
		}
	}

	for i := 0; i < len(args); i++ {
		var arg string = args[i]

		lib.Verbose(fmt.Sprintf("Processing argument: [%s]", arg), vvverbose)

		if arg == "-d" || arg == "--dir" {
			if i+1 < len(args) {
				for j := i + 1; j < len(args); j++ {
					info, err := os.Stat(args[j])
					if err != nil {
						lib.Verbose(fmt.Sprintf("Invalid directory: [%s]", args[j]), vverbose)
					} else if info.IsDir() {
						lib.Verbose(fmt.Sprintf("Adding directory to queue: [%s]", args[j]), vvverbose)
						dirs = append(dirs, args[j])
					} else {
						lib.Verbose(fmt.Sprintf("Not a directory: [%s]", args[j]), vverbose)
					}
				}
			}
		}

		if arg == "-o" || arg == "--output" {
			if i+1 < len(args) {
				outFile = args[i+1]
				i = i + 1
				continue
			}
		}

		if arg == "-q" || arg == "--quiet" {
			quiet = true
		}

		if arg == "-t" || arg == "--time" {
			procTime = true
		}
	}

	start := time.Now()
	wgroup := &sync.WaitGroup{}
	for _, dir := range dirs {
		wgroup.Add(1)
		go walkDir(dir, wgroup)
	}
	wgroup.Wait()

	filesBySizeMutex.RLock()
	for size, files := range filesBySize {
		if len(files) > 1 {
			lib.Verbose(fmt.Sprintf("Found %d files with the same size [%s (%d B)]", len(files), lib.HumanizeBytes(size, false), size), vvverbose)
			for _, file := range files {
				lib.Verbose(fmt.Sprintf("Calculate CRC32 for file: [%s]", file), vvverbose)

				crc, err := calcCrc32(file)
				if err != nil {
					lib.Verbose(fmt.Sprintf("Error calculating CRC32 for file: [%s]", file), vvverbose)
				}

				lib.Verbose(fmt.Sprintf("CRC32 for file: [%s] [%08X]", file, crc), vvverbose)

				//filesByCrcMutex.Lock()
				filesByCrc[crc] = append(filesByCrc[crc], file)
				//filesByCrcMutex.Unlock()
			}
		}
	}

	filesBySizeMutex.RUnlock()

	if outFile != "" {
		fh, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			lib.Verbose(fmt.Sprintf("error opening/creating/writing file: %s  [%v]", outFile, err), vverbose)
		} else {
			_, err = fh.WriteString("FILENAME,CRC\n")
			lib.Er(err)
		}
		defer fh.Close()
	}

	filesByCrcMutex.RLock()
	for crc, files := range filesByCrc {
		if len(files) > 1 {
			if !quiet {
				fmt.Fprintf(os.Stdout, "CRC: [%08X]\n", crc)
			}
			for _, file := range files {
				if outFile != "" {
					fh, err := os.OpenFile(outFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
					lib.Er(err)
					defer fh.Close()
					if err != nil {
						lib.Verbose(fmt.Sprintf("error opening/creating/writing file: %s  [%v]", outFile, err), vverbose)
					} else {
						_, err = fh.WriteString(fmt.Sprintf("%s,%08X\n", file, crc))
						lib.Er(err)
					}
				}
				if !quiet {
					fmt.Fprintf(os.Stdout, "  %s\n", file)
				}
			}
		}
	}
	filesByCrcMutex.RUnlock()

	stop := time.Now()
	deltaT := stop.Sub(start)

	if procTime {
		fmt.Fprintf(os.Stderr, "\n\nTotal elapsed time: %.3f s\n\n", deltaT.Seconds())
	}
}

func debug() {
	for k, v := range filesBySize {
		fmt.Fprintf(os.Stdout, "SIZE: %d\n", k)
		for _, file := range v {
			fmt.Fprintf(os.Stdout, "  FILE: %s\n", file)
		}
	}
}

func calcCrc32(file string) (uint32, error) {
	lib.Verbose(fmt.Sprintf("Open file handle for: [%s]", file), vvverbose)
	fh, err := os.Open(file)
	if err != nil {
		return 0, fmt.Errorf("Error opening file: [%s] (%v)", file, err)
	}
	defer fh.Close()

	table := crc32.MakeTable(crc32.IEEE)
	checksum := uint32(0)

	buf := make([]byte, 32*1024)
	for {
		n, err := fh.Read(buf)
		if n > 0 {
			checksum = crc32.Update(checksum, table, buf[:n])
			if verbose && term.IsTerminal(int(os.Stdout.Fd())) {
				width, _, err := term.GetSize(int(os.Stdout.Fd()))
				lib.Er(err)
				fmt.Fprintf(os.Stdout, "\033[0G\033[0K%08X  %s", checksum, lib.TruncString(file, width-10))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	fmt.Fprintf(os.Stdout, "\033[0G\033[0K")
	return checksum, nil
}
