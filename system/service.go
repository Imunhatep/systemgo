package system

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"time"
)

type Service struct {
	Name    string
	Exec    string
	Params  []string
	Restart int64

	running   *process
	history   []*process
	isStarted bool
	isStopped bool
}

func (s Service) IsNew() bool {
	return len(s.history) == 0 && s.running == nil
}

func (s Service) IsRestarting() bool {
	return s.IsFinished() && s.running == nil && s.Restart > 0
}

func (s Service) IsRunning() bool {
	return s.running != nil && s.running.Running()
}

func (s Service) IsFinished() bool {
	// no running process, but have history, or process have exited
	return (len(s.history) > 0 && s.running == nil) || (s.running != nil && s.running.Finished())
}

func (s Service) GetUsedMemory() uint64 {
	if !s.IsRunning() {
		return 0
	}

	mem, e := memoryUsage(s.running.GetPid())
	if e != nil {
		log.Println(e)
	}

	return mem
}

func (s *Service) Run(ctx context.Context, out, err chan<- string) {
	if s.isStarted {
		log.Printf("[S][%s] already running", s.Name)
		return
	}

	s.isStopped = false
	s.isStarted = true

	// do not start process if Service is exit
	for !s.IsFinished() || s.IsRestarting() {
		select {
		case <-ctx.Done():
			s.stopProcess(ctx.Err())
		case <-time.After(time.Second):
			s.handleProcess(out, err)
		}
	}

	log.Printf("[S][%s] finished", s.Name)
}

func (s *Service) handleProcess(out, err chan<- string) {
	if s.IsNew() {
		log.Printf("[S][%s] new process", s.Name)
		s.startProcess(out, err)

		return
	}

	if s.IsRestarting() {
		lastRun := s.history[len(s.history)-1]

		if time.Now().After(lastRun.Stopped.Add(time.Second * time.Duration(s.Restart))) {
			s.startProcess(out, err)
		}

		return
	}

	if s.IsFinished() {
		if s.running != nil {
			s.history = append(s.history, s.running)
			s.running = nil
		}

		if s.Restart > 0 {
			log.Printf("[S][%s] restarting in %d seconds", s.Name, s.Restart)
		}

		return
	}

	if s.IsRunning() {
		if time.Now().Second()%10 == 0 {
			mem := s.GetUsedMemory()
			log.Printf("[S][%s][%d] memory usage: %.2d kb", s.Name, s.running.GetPid(), mem/1024)
		}
	}
}

func (s *Service) startProcess(out, err chan<- string) {
	running := NewProcess(s.Name, s.Exec, s.Params)

	started := make(chan error)
	go running.Start(started)
	<-started

	s.running = running

	// listen for STD
	s.scanProcessStd("%s", &s.running.Out, out)
	s.scanProcessStd("error: %s", &s.running.Err, err)
}

func (s *Service) stopProcess(err error) error {
	if s.isStopped {
		log.Printf("[S][%s] service.Stop() already have been called", s.Name)
		return nil
	}

	log.Printf("[S][%s] %s", s.Name, err)
	time.Sleep(time.Second * 1)

	// disable restarting
	s.Restart = 0

	s.isStopped = true
	if s.IsRunning() {
		return s.running.Stop()
	}

	return nil
}

func (s Service) scanProcessStd(format string, src *io.ReadCloser, dst chan<- string) {
	stdScanner := bufio.NewScanner(*src)

	go func() {
		for s.IsRunning() && stdScanner.Scan() {
			logs := stdScanner.Text()
			dst <- fmt.Sprintf("[%s] "+format, s.Name, logs)
		}
	}()
}
