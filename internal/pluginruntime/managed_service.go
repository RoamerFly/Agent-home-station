package pluginruntime

// Managed services are trusted host adapters, not plugin subprocess launch
// requests. Only host code may resolve a verified installation into a Spec;
// neither JSON-RPC arguments nor plugin configuration is a command descriptor.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ManagedServiceSpec struct {
	// OwnerPlugin is fixed by the trusted host adapter, never RPC arguments.
	OwnerPlugin string
	// Binding identifies the exact verified installation/configuration snapshot.
	Binding         string
	Executable      string
	Arguments       []string
	Directory       string
	Environment     []string
	LogPath         string
	PendingJobsPath string
	StopInput       string
	StopTimeout     time.Duration
	// Check must revalidate current host registration. It is checked even when
	// a process already exists, so changed configuration cannot adopt it.
	Check func(context.Context) error
}

// ManagedServiceAdmission serializes command.Start with the host's drain/update
// barrier. It must invoke start exactly once, synchronously, only if admitted.
type ManagedServiceAdmission func(context.Context, func() error) error

type ManagedServiceStatus struct {
	Running  bool `json:"running"`
	Stopping bool `json:"stopping"`
	Busy     bool `json:"busy"`
}

type managedService struct {
	spec     ManagedServiceSpec
	command  *exec.Cmd
	input    io.WriteCloser
	done     chan struct{}
	stopOnce sync.Once
	stopping bool
}

// ManagedServices owns only processes started by this registry. There is no
// PID import, process-name lookup, listener adoption, or arbitrary kill API.
// The zero value is ready to use.
type ManagedServices struct {
	mu         sync.Mutex
	processes  map[string]*managedService
	ownerStops map[string]int
	closed     bool
}

func (r *ManagedServices) Start(ctx context.Context, id string, spec ManagedServiceSpec, admit ManagedServiceAdmission) (ManagedServiceStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ManagedServiceStatus{}, errors.New("managed_service_registry_closed")
	}
	if err := ctx.Err(); err != nil {
		return ManagedServiceStatus{}, err
	}
	if id == "" || spec.OwnerPlugin == "" || spec.Binding == "" || spec.Check == nil || admit == nil || !filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.Directory) || !filepath.IsAbs(spec.LogPath) || spec.StopTimeout <= 0 || len(spec.Environment) == 0 {
		return ManagedServiceStatus{}, errors.New("managed_service_descriptor_invalid")
	}
	if r.ownerStops[spec.OwnerPlugin] > 0 {
		return ManagedServiceStatus{}, errors.New("managed_service_owner_stopping")
	}
	if err := spec.Check(ctx); err != nil {
		return ManagedServiceStatus{}, err
	}
	if p := r.processes[id]; p != nil {
		select {
		case <-p.done:
			delete(r.processes, id)
		default:
			if p.spec.Binding != spec.Binding || p.spec.OwnerPlugin != spec.OwnerPlugin {
				return ManagedServiceStatus{}, errors.New("managed_service_registration_changed_stop_required")
			}
			if p.stopping {
				return statusManagedService(p), errors.New("managed_service_stopping")
			}
			return statusManagedService(p), nil
		}
	}
	command := exec.Command(spec.Executable, append([]string(nil), spec.Arguments...)...)
	command.Dir, command.Env = spec.Directory, append([]string(nil), spec.Environment...)
	configureManagedService(command)
	input, err := command.StdinPipe()
	if err != nil {
		return ManagedServiceStatus{}, err
	}
	log, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		input.Close()
		return ManagedServiceStatus{}, err
	}
	command.Stdout, command.Stderr = log, log
	if err = spec.Check(ctx); err == nil {
		invoked := false
		err = admit(ctx, func() error {
			if invoked {
				return errors.New("managed_service_duplicate_admission")
			}
			invoked = true
			return command.Start()
		})
		if err == nil && command.Process == nil {
			err = errors.New("managed_service_not_started")
		}
	}
	if command.Process == nil {
		input.Close()
		log.Close()
		return ManagedServiceStatus{}, err
	}
	p := &managedService{spec: spec, command: command, input: input, done: make(chan struct{})}
	if r.processes == nil {
		r.processes = make(map[string]*managedService)
	}
	r.processes[id] = p
	go func() {
		_ = command.Wait()
		input.Close()
		log.Close()
		close(p.done)
	}()
	if err != nil {
		// A host admission callback must not lose ownership if it reports an
		// error after starting. Retain/reap and stop that exact owned process.
		p.stopping = true
		_ = command.Process.Kill()
		return statusManagedService(p), err
	}
	return statusManagedService(p), nil
}

func statusManagedService(p *managedService) ManagedServiceStatus {
	if p == nil {
		return ManagedServiceStatus{}
	}
	select {
	case <-p.done:
		return ManagedServiceStatus{}
	default:
	}
	return ManagedServiceStatus{Running: true, Stopping: p.stopping, Busy: pendingManagedServiceJobs(p.spec.PendingJobsPath)}
}

// A missing journal means no submitted jobs. An unreadable, oversized or
// malformed journal cannot prove idleness and conservatively blocks draining.
func pendingManagedServiceJobs(path string) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil || len(data) > 32<<20 {
		return true
	}
	var jobs map[string]*struct {
		State string `json:"state"`
	}
	if json.Unmarshal(data, &jobs) != nil || jobs == nil {
		return true
	}
	for _, job := range jobs {
		if job == nil || job.State == "" {
			return true
		}
		if strings.EqualFold(job.State, "queued") || strings.EqualFold(job.State, "running") {
			return true
		}
	}
	return false
}

func (r *ManagedServices) Status(id string) ManagedServiceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return statusManagedService(r.processes[id])
}

// Busy lets account removal/update barriers cover every registered adapter,
// without adding a new typed Manager field for each service.
func (r *ManagedServices) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, process := range r.processes {
		if statusManagedService(process).Busy {
			return true
		}
	}
	return false
}

func (r *ManagedServices) OwnerBusy(owner string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, process := range r.processes {
		if process.spec.OwnerPlugin == owner && statusManagedService(process).Busy {
			return true
		}
	}
	return false
}

// StopOwner refuses a busy owner before stopping any of its services. It
// reserves that owner against new launches while waiting, and captures owned
// process pointers so an exited/reused registry ID cannot target another owner.
func (r *ManagedServices) StopOwner(ctx context.Context, owner string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == "" {
		return errors.New("managed_service_owner_invalid")
	}
	r.mu.Lock()
	processes := make([]*managedService, 0)
	for _, process := range r.processes {
		if process.spec.OwnerPlugin != owner {
			continue
		}
		if statusManagedService(process).Busy {
			r.mu.Unlock()
			return errors.New("managed_service_owner_busy")
		}
		processes = append(processes, process)
	}
	if r.ownerStops == nil {
		r.ownerStops = make(map[string]int)
	}
	r.ownerStops[owner]++
	for _, process := range processes {
		process.stopping = true
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.ownerStops[owner]--
		if r.ownerStops[owner] == 0 {
			delete(r.ownerStops, owner)
		}
		r.mu.Unlock()
	}()
	return stopManagedServices(ctx, processes)
}

func (r *ManagedServices) Stop(ctx context.Context, id string) error {
	r.mu.Lock()
	p := r.processes[id]
	if p == nil {
		r.mu.Unlock()
		return nil
	}
	p.stopping = true
	r.mu.Unlock()
	return stopManagedService(ctx, p)
}

func stopManagedService(ctx context.Context, p *managedService) error {
	p.stopOnce.Do(func() {
		go func() {
			// Closing stdin also tells a supervisor to stop if its protocol input
			// cannot be written. The timeout kills only this owned supervisor.
			if p.spec.StopInput != "" {
				_, _ = io.WriteString(p.input, p.spec.StopInput)
			}
			p.input.Close()
		}()
	})
	timer := time.NewTimer(p.spec.StopTimeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		_ = p.command.Process.Kill()
	}
	// Wait is bounded even if the operating system fails to reap a process.
	timer.Reset(5 * time.Second)
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("managed_service_stop_unconfirmed")
	}
}

func (r *ManagedServices) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	processes := make([]*managedService, 0, len(r.processes))
	for _, process := range r.processes {
		process.stopping = true
		processes = append(processes, process)
	}
	r.mu.Unlock()
	return stopManagedServices(ctx, processes)
}

func stopManagedServices(ctx context.Context, processes []*managedService) error {
	var joined error
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, process := range processes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := stopManagedService(ctx, process)
			mu.Lock()
			joined = errors.Join(joined, err)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return joined
}
