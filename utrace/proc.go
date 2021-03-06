package utrace

import (
	"errors"
	"os"
	"os/exec"

	"github.com/zyedidia/perforator/utrace/ptrace"
	"golang.org/x/sys/unix"
)

var (
	interrupt = []byte{0xCC}

	ErrInvalidBreakpoint = errors.New("Invalid breakpoint")
)

// A Proc is a single instance of a traced process. On Linux this may be a
// process or a thread (they are equivalent, except for the visible address
// space).
type Proc struct {
	tracer    *ptrace.Tracer
	regions   []activeRegion
	pieOffset uint64
	exited    bool

	breakpoints map[uintptr][]byte
}

// Starts a new process from the given information and begins tracing.
func startProc(pie PieOffsetter, target string, args []string, regions []Region) (*Proc, error) {
	cmd := exec.Command(target, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &unix.SysProcAttr{
		Ptrace: true,
	}

	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	// wait for execve
	cmd.Wait()

	options := unix.PTRACE_O_EXITKILL | unix.PTRACE_O_TRACECLONE |
		unix.PTRACE_O_TRACEFORK | unix.PTRACE_O_TRACEVFORK |
		unix.PTRACE_O_TRACEEXEC

	p, err := newTracedProc(cmd.Process.Pid, pie, regions, nil)
	if err != nil {
		return nil, err
	}
	err = p.tracer.ReAttachAndContinue(options)
	if err != nil {
		return nil, err
	}

	// Wait for the initial SIGTRAP created because we are attaching
	// with ReAttachAndContinue to properly handle group stops.
	var ws unix.WaitStatus
	_, err = unix.Wait4(p.tracer.Pid(), &ws, 0, nil)
	if err != nil {
		return nil, err
	} else if ws.StopSignal() != unix.SIGTRAP {
		return nil, errors.New("wait: received non SIGTRAP: " + ws.StopSignal().String())
	}
	err = p.cont(0, false)

	return p, err
}

// Begins tracing an already existing process
func newTracedProc(pid int, pie PieOffsetter, regions []Region, breaks map[uintptr][]byte) (*Proc, error) {
	off, err := pie.PieOffset(pid)
	if err != nil {
		return nil, err
	}

	logger.Printf("%d: PIE offset is 0x%x\n", pid, off)

	p := &Proc{
		tracer:      ptrace.NewTracer(pid),
		regions:     make([]activeRegion, 0, len(regions)),
		pieOffset:   off,
		breakpoints: make(map[uintptr][]byte),
	}

	for id, r := range regions {
		addr := uintptr(r.Start(p))
		if orig, ok := breaks[addr]; ok {
			p.breakpoints[addr] = make([]byte, len(orig))
			copy(p.breakpoints[addr], orig)
		} else {
			err := p.setBreak(r.Start(p))
			if err != nil {
				return nil, err
			}
		}

		p.regions = append(p.regions, activeRegion{
			region:       r,
			state:        RegionStart,
			curInterrupt: r.Start(p),
			id:           id,
		})
	}

	return p, nil
}

func (p *Proc) setBreak(pc uint64) error {
	var err error
	pcptr := uintptr(pc)

	if _, ok := p.breakpoints[pcptr]; ok {
		// breakpoint already exists
		return nil
	}

	orig := make([]byte, len(interrupt))
	_, err = p.tracer.PeekData(pcptr, orig)
	if err != nil {
		return err
	}
	_, err = p.tracer.PokeData(pcptr, interrupt)
	if err != nil {
		return err
	}

	p.breakpoints[pcptr] = orig
	return nil
}

func (p *Proc) removeBreak(pc uint64) error {
	pcptr := uintptr(pc)
	orig, ok := p.breakpoints[pcptr]
	if !ok {
		return ErrInvalidBreakpoint
	}
	_, err := p.tracer.PokeData(pcptr, orig)
	delete(p.breakpoints, pcptr)
	return err
}

// An Event represents a change in the state of a traced region. This may be an
// enter or an exit.
type Event struct {
	Id    int
	State RegionState
}

func (p *Proc) handleInterrupt() ([]Event, error) {
	var regs unix.PtraceRegs
	p.tracer.GetRegs(&regs)
	regs.Rip -= uint64(len(interrupt))
	p.tracer.SetRegs(&regs)

	logger.Printf("%d: interrupt at 0x%x\n", p.Pid(), regs.Rip)

	err := p.removeBreak(regs.Rip)
	if err != nil {
		return nil, err
	}

	events := make([]Event, 0)
	for i, r := range p.regions {
		var err error
		if r.curInterrupt == regs.Rip {
			events = append(events, Event{
				Id:    r.id,
				State: r.state,
			})
			switch r.state {
			case RegionStart:
				p.regions[i].state = RegionEnd
				var addr uint64
				addr, err = r.region.End(regs.Rsp, p)
				if err != nil {
					return nil, err
				}
				p.regions[i].curInterrupt = addr
				err = p.setBreak(addr)
			case RegionEnd:
				p.regions[i].state = RegionStart
				p.regions[i].curInterrupt = r.region.Start(p)
				err = p.setBreak(p.regions[i].curInterrupt)
			default:
				return nil, errors.New("invalid state")
			}
		}
		if err != nil {
			return nil, err
		}
	}

	return events, nil
}

func (p *Proc) cont(sig unix.Signal, groupStop bool) error {
	if p.exited {
		return nil
	}
	if groupStop {
		return p.tracer.Listen()
	}
	return p.tracer.Cont(sig)
}

func (p *Proc) exit() {
	p.exited = true
}

// Pid returns this process's PID.
func (p *Proc) Pid() int {
	return p.tracer.Pid()
}
