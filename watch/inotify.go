// Copyright (c) 2013 ActiveState Software Inc. All rights reserved.

package watch

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ActiveState/tail/util"
	"gopkg.in/fsnotify.v0"
	"gopkg.in/tomb.v1"
)

// InotifyFileWatcher uses inotify to monitor file changes.
type InotifyFileWatcher struct {
	Filename string
	Size     int64
	w        *fsnotify.Watcher
}

func NewInotifyFileWatcher(filename string, w *fsnotify.Watcher) *InotifyFileWatcher {
	fw := &InotifyFileWatcher{filename, 0, w}
	return fw
}

func (fw *InotifyFileWatcher) BlockUntilExists(t *tomb.Tomb) error {
	dirname := filepath.Dir(fw.Filename)

	// Watch for new files to be created in the parent directory.
	err := fw.w.WatchFlags(dirname, fsnotify.FSN_CREATE)
	if err != nil {
		return err
	}
	defer fw.w.RemoveWatch(dirname)

	// Do a real check now as the file might have been created before
	// calling `WatchFlags` above.
	if _, err = os.Stat(fw.Filename); !os.IsNotExist(err) {
		// file exists, or stat returned an error.
		return err
	}

	for {
		select {
		case evt, ok := <-fw.w.Event:
			if !ok {
				return fmt.Errorf("inotify watcher has been closed")
			} else if filepath.Base(evt.Name) == filepath.Base(fw.Filename) {
				return nil
			}
		case <-t.Dying():
			return tomb.ErrDying
		}
	}
	panic("unreachable")
}

func (fw *InotifyFileWatcher) ChangeEvents(t *tomb.Tomb, fi os.FileInfo) *FileChanges {
	changes := NewFileChanges()

	if err := fw.w.Watch(fw.Filename); err != nil {
		util.Fatal("Error watching %v: %v", fw.Filename, err)
	}

	// Watch the directory to be notified when the file is deleted since the file
	// watch is on the inode, not the path.
	dirname := filepath.Dir(fw.Filename)
	if err := fw.w.WatchFlags(dirname, fsnotify.FSN_DELETE); err != nil {
		util.Fatal("Error watching %v: %v", dirname, err)
	}

	fw.Size = fi.Size()

	go func() {
		defer fw.w.RemoveWatch(fw.Filename)
		defer fw.w.RemoveWatch(dirname)
		defer changes.Close()

		for {
			prevSize := fw.Size

			var evt *fsnotify.FileEvent
			var ok bool

			select {
			case evt, ok = <-fw.w.Event:
				if !ok {
					return
				}
			case <-t.Dying():
				return
			}

			switch {
			case evt.IsDelete():
				if filepath.Base(evt.Name) != filepath.Base(fw.Filename) {
					continue
				}
				fallthrough

			case evt.IsRename():
				changes.NotifyDeleted()
				return

			case evt.IsModify():
				fi, err := os.Stat(fw.Filename)
				if err != nil {
					if os.IsNotExist(err) {
						changes.NotifyDeleted()
						return
					}
					// XXX: report this error back to the user
					util.Fatal("Failed to stat file %v: %v", fw.Filename, err)
				}
				fw.Size = fi.Size()

				if prevSize > 0 && prevSize > fw.Size {
					changes.NotifyTruncated()
				} else {
					changes.NotifyModified()
				}
				prevSize = fw.Size
			}
		}
	}()

	return changes
}
