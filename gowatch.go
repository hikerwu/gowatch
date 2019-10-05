package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	path "path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/howeyc/fsnotify"
	"github.com/silenceper/log"
)

var (
	cmd          *exec.Cmd
	state        sync.Mutex
	eventTime    = make(map[string]int64)
	scheduleTime time.Time
)

//NewWatcher new watcher
func NewWatcher(paths []string, files []string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Errorf(" Fail to create new Watcher[ %s ]\n", err)
		os.Exit(2)
	}

	go func() {
		for {
			select {
			case e := <-watcher.Event:
				isbuild := true

				// Skip ignored files
				if shouldIgnoreFile(e.Name) {
					continue
				}
				if !checkIfWatchExt(e.Name) {
					continue
				}
				if checkOtherIgnoreFile(e.Name) {
					continue
				}

				mt := getFileModTime(e.Name)
				if t := eventTime[e.Name]; mt == t {
					//log.Infof("[SKIP] # %s #\n", e.String())
					isbuild = false
				}

				eventTime[e.Name] = mt

				if isbuild {
					go func() {
						// Wait 1s before autobuild util there is no file change.
						scheduleTime = time.Now().Add(1 * time.Second)
						for {
							time.Sleep(scheduleTime.Sub(time.Now()))
							if time.Now().After(scheduleTime) {
								break
							}
							return
						}

						Autobuild(files)
					}()
				}
			case err := <-watcher.Error:
				log.Errorf("%v", err)
				log.Warnf(" %s\n", err.Error()) // No need to exit here
			}
		}
	}()

	log.Infof("Initializing watcher...\n")
	for _, path := range paths {
		log.Infof("Directory( %s )\n", path)
		err = watcher.Watch(path)
		if err != nil {
			log.Errorf("Fail to watch directory[ %s ]\n", err)
			os.Exit(2)
		}
	}

}

// getFileModTime retuens unix timestamp of `os.File.ModTime` by given path.
func getFileModTime(path string) int64 {
	path = strings.Replace(path, "\\", "/", -1)
	f, err := os.Open(path)
	if err != nil {
		log.Errorf("Fail to open file[ %s ]\n", err)
		return time.Now().Unix()
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		log.Errorf("Fail to get file information[ %s ]\n", err)
		return time.Now().Unix()
	}

	return fi.ModTime().Unix()
}

//获取文件修改时间 返回unix时间戳
func GetFileModTime(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		log.Errorf("open file error:%s", path)
		return 0
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		log.Errorf("open file error:%s", path)
		return 0
	}

	// "2006-01-02 15:04:05.000000"
	// log.Infof("%s:%s", path, fi.ModTime().Format("2006-01-02 15:04:05.000000"))
	return fi.ModTime().UnixNano()
}

// 自动启动go generate
func RunGenerate() bool {
	log.Infof("Start building...\n")

	cmdName := "go"

	var err error

	args := []string{"generate"}
	for _, one := range cfg.GenerateDir {
		// 检查文件更新时间
		// 生成文件比编辑文件新,则不用触发啦;
		mtime := GetFileModTime(one.Model)
		otime := GetFileModTime(one.Output)
		if otime > 0 && mtime < otime {
			log.Infof("file no need to restart go generate:%d-%d %s\n", mtime, otime, one.Model)
			continue
		}
		args = append(args, one.Model)
	}

	if len(args) == 1 {
		// 都被过滤了,所以就当时成功了吧;
		return true
	}

	bcmd := exec.Command(cmdName, args...)
	bcmd.Env = os.Environ()
	bcmd.Stdout = os.Stdout
	bcmd.Stderr = os.Stderr
	log.Infof("Run Args: %s %s", cmdName, strings.Join(args, " "))
	err = bcmd.Run()

	if err != nil {
		log.Errorf("============== Generate failed ===================\n")
		log.Errorf("%+v\n", err)
		return false
	}
	log.Infof("Generate was successful\n")
	return true
}

func RunCMD(cmdSt *PreCMDSt) bool {
	log.Infof("Start running...\n")

	cmdName := cmdSt.CMD

	var err error

	args := []string{}
	args = append(args, cmdSt.Args...)

	bcmd := exec.Command(cmdName, args...)
	bcmd.Env = os.Environ()
	bcmd.Stdout = os.Stdout
	bcmd.Stderr = os.Stderr
	log.Infof("Run: %s Args: %s", cmdName, strings.Join(args, " "))
	err = bcmd.Run()

	if err != nil {
		log.Errorf("============== %s failed ===================\n", cmdSt.CMD)
		log.Errorf("%+v\n", err)
		return false
	}
	log.Infof("%s was successful\n", cmdSt.CMD)
	return true
}

//Autobuild auto build
func Autobuild(files []string) {
	state.Lock()
	defer state.Unlock()

	log.Infof("Start building...\n")

	if err := os.Chdir(currpath); err != nil {
		log.Errorf("Chdir Error: %+v\n", err)
		return
	}

	// 运行前置命令;
	if len(cfg.PreCMDs) > 0 {
		for _, cmd := range cfg.PreCMDs {
			if !RunCMD(cmd) {
				return
			}
		}
	}

	// go generate
	if !RunGenerate() {
		return
	}

	cmdName := "go"

	var err error

	args := []string{"build"}
	args = append(args, "-o", cfg.Output)
	args = append(args, cfg.BuildArgs...)
	if cfg.BuildTags != "" {
		args = append(args, "-tags", cfg.BuildTags)
	}
	args = append(args, files...)

	bcmd := exec.Command(cmdName, args...)
	bcmd.Env = append(os.Environ(), "GOGC=off")
	bcmd.Stdout = os.Stdout
	bcmd.Stderr = os.Stderr
	log.Infof("Build Args: %s %s", cmdName, strings.Join(args, " "))
	err = bcmd.Run()

	if err != nil {
		log.Errorf("============== Build failed ===================\n")
		log.Errorf("%+v\n", err)
		return
	}
	log.Infof("Build was successful\n")
	if !cfg.DisableRun {
		Restart(cfg.Output)
	}
}

//Kill kill process
func Kill() {
	defer func() {
		if e := recover(); e != nil {
			fmt.Println("Kill.recover -> ", e)
		}
	}()
	if cmd != nil && cmd.Process != nil {
		err := cmd.Process.Kill()
		if err != nil {
			fmt.Println("Kill -> ", err)
		}
	}
}

//Restart restart app
func Restart(appname string) {
	//log.Debugf("kill running process")
	Kill()
	go Start(appname)
}

//Start start app
func Start(appname string) {
	log.Infof("Restarting %s ...\n", appname)
	if strings.Index(appname, "./") == -1 {
		appname = "./" + appname
	}

	cmd = exec.Command(appname)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Args = append([]string{appname}, cfg.CmdArgs...)
	cmd.Env = append(os.Environ(), cfg.Envs...)
	log.Infof("Run %s", strings.Join(cmd.Args, " "))
	go cmd.Run()

	log.Infof("%s is running...\n", appname)
	started <- true
}

// Should ignore filenames generated by
// Emacs, Vim or SublimeText
func shouldIgnoreFile(filename string) bool {
	for _, regex := range ignoredFilesRegExps {
		r, err := regexp.Compile(regex)
		if err != nil {
			panic("Could not compile the regex: " + regex)
		}
		if r.MatchString(filename) {
			return true
		}
		continue
	}
	return false
}

// checkIfWatchExt returns true if the name HasSuffix <watch_ext>.
func checkIfWatchExt(name string) bool {
	for _, s := range cfg.WatchExts {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// 如果是generate的自动文件,则自动忽略;避免二次编译;
func checkOtherIgnoreFile(filename string) bool {
	for _, fl := range cfg.GenerateDir {
		absP, err := path.Abs(fl.Output)
		if err != nil {
			log.Errorf("err =%v", err)
			log.Errorf("Can not get absolute path of [ %s ]\n", fl.Output)
			continue
		}
		absFilePath, err := path.Abs(filename)
		if err != nil {
			log.Errorf("Can not get absolute path of [ %s ]\n", filename)
			break
		}
		if strings.HasPrefix(absFilePath, absP) {
			log.Infof("Excluding file from watching [ %s ]\n", filename)
			return true
		}
	}
	return false
}

func readAppDirectories(directory string, paths *[]string) {
	fileInfos, err := ioutil.ReadDir(directory)
	if err != nil {
		return
	}

	useDirectory := false
	for _, fileInfo := range fileInfos {
		if strings.HasSuffix(fileInfo.Name(), "docs") {
			continue
		}
		if strings.HasSuffix(fileInfo.Name(), "swagger") {
			continue
		}

		if !cfg.VendorWatch && strings.HasSuffix(fileInfo.Name(), "vendor") {
			continue
		}

		if isExcluded(path.Join(directory, fileInfo.Name())) {
			continue
		}

		if fileInfo.IsDir() == true && fileInfo.Name()[0] != '.' {
			readAppDirectories(directory+"/"+fileInfo.Name(), paths)
			continue
		}
		if useDirectory == true {
			continue
		}
		*paths = append(*paths, directory)
		useDirectory = true
	}
	return
}

// If a file is excluded
func isExcluded(filePath string) bool {
	for _, p := range cfg.ExcludedPaths {
		absP, err := path.Abs(p)
		if err != nil {
			log.Errorf("err =%v", err)
			log.Errorf("Can not get absolute path of [ %s ]\n", p)
			continue
		}
		absFilePath, err := path.Abs(filePath)
		if err != nil {
			log.Errorf("Can not get absolute path of [ %s ]\n", filePath)
			break
		}
		if strings.HasPrefix(absFilePath, absP) {
			log.Infof("Excluding from watching [ %s ]\n", filePath)
			return true
		}
	}
	return false
}
