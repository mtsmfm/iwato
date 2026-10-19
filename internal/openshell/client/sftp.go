package client

import (
	"io"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP is a sandbox's SSH connection on which SFTP channels open (the
// supervisor's "sftp" subsystem, rooted at the sandbox's workdir).
type SFTP struct{ *ssh.Client }

// OpenSFTP opens one more SFTP channel (an SSH session on the "sftp"
// subsystem): what the server sends, and what goes to it. Closing the
// latter ends the session at once (not after the server's EOF, which
// OpenShell sends only after 30-60 s).
func (s SFTP) OpenSFTP() (io.Reader, io.WriteCloser, error) {
	sess, err := s.NewSession()
	if err != nil {
		return nil, nil, err
	}
	w, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, nil, err
	}
	r, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, nil, err
	}
	e, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		return nil, nil, err
	}
	if err := sess.RequestSubsystem("sftp"); err != nil {
		sess.Close()
		return nil, nil, err
	}
	// Unread stderr would hold the channel's window.
	go io.Copy(io.Discard, e)
	return r, sessionWriter{w, sess}, nil
}

// ClientOptions: OpenShell takes packets up to 256 KiB; 64 KiB ones leave
// room for the headers.
func (s SFTP) ClientOptions() []sftp.ClientOption {
	return []sftp.ClientOption{sftp.MaxPacketUnchecked(64 << 10), sftp.UseConcurrentWrites(true)}
}

type sessionWriter struct {
	io.WriteCloser
	s *ssh.Session
}

func (w sessionWriter) Close() error {
	err := w.WriteCloser.Close()
	_ = w.s.Close()
	return err
}
