package main

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/ViaQ/logwatcher/internal/inotify"
	"github.com/golang/glog"
)

var (
	watchDirFlag = flag.String("watch_dir", func() string {
		wd, _ := os.Getwd()
		return wd
	}(), "directory to watch for logs")
)

func main() {
	flag.Parse()
	watchDir, err := filepath.Abs(*watchDirFlag)
	if err != nil {
		glog.Exit("error in arguments.", os.Args)
	}
	d, err := os.Stat(watchDir)
	if err != nil {
		glog.Exitf("Error occured in inputs. Error: %v", err)
		return
	}
	if !d.IsDir() {
		glog.Exitf("watch_dir must be a directory.")
		return
	}
	glog.V(0).Infof("Watching directory %s", watchDir)
	n, err := inotify.New(watchDir)
	if err != nil {
		glog.Exit("error in starting watcher: ", err)
		os.Exit(-1)
	}

	n.Start()

	glog.Info("Bye..")
}
