package internal

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
	err := log.openF()
	if err != nil {
		return nil, err
	}
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
			return -1, err
		}
	}
	return n, nil
}

func (log *Logger) Close() error {
	return log.f.Close()
}

func (log *Logger) openF() error {
	f, err := os.OpenFile(
		log.folder+"/"+
			time.Now().Format(dateFmt)+"-discordyt.log",
		os.O_WRONLY|os.O_CREATE|os.O_APPEND,
		0644,
	)
	if err != nil {
		return err
	}
	log.f = f
	log.num = 0
	return nil
}
