package splitlog

import (
	"os"
	"time"
)

const maxWrites = 10000
const dateFmt = "2006-01-02T15:04:05"

type Logger struct {
	f      *os.File
	folder string
	num    int
}

func Open(logFolder string) (*Logger, error) {
	log := &Logger{}
	log.folder = logFolder
	oldstderr := os.Stderr
	err := log.openF()
	if err != nil {
		os.Stderr = oldstderr
		return nil, err
	}
	oldstderr.Close()
	return log, nil
}

func (log *Logger) Write(p []byte) (n int, err error) {
	n, err = log.f.Write(p)
	if err != nil {
		return n, err
	}
	log.num += 1
	if log.num >= maxWrites {
		log.f.Close()
		err = log.openF()
		if err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (log *Logger) Close() error {
	return log.f.Close()
}

func (log *Logger) openF() error {
	f, err := os.OpenFile(
		log.folder+"/discordyt-"+
			time.Now().Format(dateFmt)+".log",
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		0644,
	)
	if err != nil {
		return err
	}
	os.Stderr = f
	log.f = f
	log.num = 0
	return nil
}
