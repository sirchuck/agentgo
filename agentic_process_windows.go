//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	agenticCreateNewProcessGroup        = 0x00000200
	agenticJobObjectExtendedLimitInfo   = 9
	agenticJobObjectLimitKillOnJobClose = 0x00002000
	agenticProcessTerminate             = 0x0001
	agenticProcessSetQuota              = 0x0100
	agenticProcessQueryLimitedInfo      = 0x1000
	agenticCtrlBreakEvent               = 1
)

type agenticIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}
type agenticJobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}
type agenticJobExtendedLimitInformation struct {
	BasicLimitInformation agenticJobBasicLimitInformation
	IoInfo                agenticIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

var (
	agenticKernel32                 = syscall.NewLazyDLL("kernel32.dll")
	agenticCreateJobObjectW         = agenticKernel32.NewProc("CreateJobObjectW")
	agenticSetInformationJobObject  = agenticKernel32.NewProc("SetInformationJobObject")
	agenticAssignProcessToJobObject = agenticKernel32.NewProc("AssignProcessToJobObject")
	agenticTerminateJobObject       = agenticKernel32.NewProc("TerminateJobObject")
	agenticOpenProcess              = agenticKernel32.NewProc("OpenProcess")
	agenticGenerateConsoleCtrlEvent = agenticKernel32.NewProc("GenerateConsoleCtrlEvent")
)

type agenticProcessController struct{ job syscall.Handle }

func newAgenticProcessController(cmd *exec.Cmd) (*agenticProcessController, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: agenticCreateNewProcessGroup}
	job, _, callErr := agenticCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return nil, fmt.Errorf("CreateJobObjectW failed: %v", callErr)
	}
	controller := &agenticProcessController{job: syscall.Handle(job)}
	info := agenticJobExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = agenticJobObjectLimitKillOnJobClose
	ok, _, setErr := agenticSetInformationJobObject.Call(job, agenticJobObjectExtendedLimitInfo, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		_ = controller.Close()
		return nil, fmt.Errorf("SetInformationJobObject failed: %v", setErr)
	}
	return controller, nil
}
func (c *agenticProcessController) AfterStart(cmd *exec.Cmd) error {
	if c == nil || c.job == 0 || cmd == nil || cmd.Process == nil {
		return fmt.Errorf("process containment is unavailable")
	}
	process, _, openErr := agenticOpenProcess.Call(agenticProcessTerminate|agenticProcessSetQuota|agenticProcessQueryLimitedInfo, 0, uintptr(cmd.Process.Pid))
	if process == 0 {
		return fmt.Errorf("OpenProcess failed: %v", openErr)
	}
	defer syscall.CloseHandle(syscall.Handle(process))
	ok, _, assignErr := agenticAssignProcessToJobObject.Call(uintptr(c.job), process)
	if ok == 0 {
		return fmt.Errorf("AssignProcessToJobObject failed: %v", assignErr)
	}
	return nil
}
func (c *agenticProcessController) Graceful(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_, _, _ = agenticGenerateConsoleCtrlEvent.Call(agenticCtrlBreakEvent, uintptr(cmd.Process.Pid))
	}
}
func (c *agenticProcessController) Force(cmd *exec.Cmd) {
	if c != nil && c.job != 0 {
		_, _, _ = agenticTerminateJobObject.Call(uintptr(c.job), 1)
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func (c *agenticProcessController) Close() error {
	if c == nil || c.job == 0 {
		return nil
	}
	err := syscall.CloseHandle(c.job)
	c.job = 0
	return err
}
