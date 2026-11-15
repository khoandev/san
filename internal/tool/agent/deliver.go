package agent

import (
	"fmt"

	"github.com/genai-io/san/internal/broker"
	"github.com/genai-io/san/internal/task"
)

// Delivery is the mail a sender hands to DeliverToRunning: where it goes, who
// owns it, and which native message it came from. SourceSession is empty for
// host-generated mail.
type Delivery struct {
	TaskID        string
	ParentSession string
	SourceSession string
	SourceSeq     uint64
	Subject       string
	Message       string
}

// DeliverToRunning queues a message into a running worker's inbox. It is the
// one delivery path shared by the SendMessage tool and a host that steers a
// worker directly, so a worker cannot tell the two apart and the envelope is
// never duplicated.
//
// The manager is the caller's: the tool reads the process-wide one, while a
// host may own a manager of its own. Only a running agent task of this
// conversation is accepted — a terminal one needs the continuation gate, which
// belongs to the tool's approval path, and no caller may reach around it.
func DeliverToRunning(managed *task.Manager, mail Delivery) error {
	// A route can outlive its task record (a session-hosted worker, a peer that
	// registered directly), so an unknown task is not a delivery verdict; the
	// broker still gets the attempt. A known one is checked first, because a
	// terminal worker must never be reached as if it were live.
	if managed != nil {
		if worker, found := managed.Get(mail.TaskID); found {
			info := worker.GetStatus()
			if info.Type != task.TaskTypeAgent {
				return fmt.Errorf("task %q is a %s task, not an agent with an inbox", mail.TaskID, info.Type)
			}
			if info.ParentSessionID != "" && mail.ParentSession != "" && info.ParentSessionID != mail.ParentSession {
				return fmt.Errorf("task %q belongs to a different parent session", mail.TaskID)
			}
			if info.Status != task.StatusRunning {
				return fmt.Errorf("worker %q is not running (status %s)", mail.TaskID, info.Status)
			}
		}
	}
	from := broker.Main
	display := brokerSubject(from, mail.Subject)
	delivered := broker.Send(broker.Message{
		SourceSession: mail.SourceSession,
		SourceSeq:     mail.SourceSeq,
		SessionID:     mail.ParentSession,
		From:          from,
		To:            mail.TaskID,
		Subject:       display,
		Content:       wrapAgentMessage(from, display, mail.Message),
	})
	if !delivered {
		return fmt.Errorf("worker %q is starting up or its inbox is momentarily full", mail.TaskID)
	}
	return nil
}
