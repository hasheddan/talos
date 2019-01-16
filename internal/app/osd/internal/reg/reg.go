/* This Source Code Form is subject to the terms of the Mozilla Public
 * License, v. 2.0. If a copy of the MPL was not distributed with this
 * file, You can obtain one at http://mozilla.org/MPL/2.0/. */

package reg

import (
	"context"
	"io/ioutil"
	"log"
	"os"

	"github.com/autonomy/talos/internal/app/init/pkg/system/runner"
	containerdrunner "github.com/autonomy/talos/internal/app/init/pkg/system/runner/containerd"
	"github.com/autonomy/talos/internal/app/osd/proto"
	filechunker "github.com/autonomy/talos/internal/pkg/chunker/file"
	"github.com/autonomy/talos/internal/pkg/constants"
	"github.com/autonomy/talos/internal/pkg/userdata"
	"github.com/autonomy/talos/internal/pkg/version"
	"github.com/containerd/cgroups"
	"github.com/containerd/containerd"
	tasks "github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/defaults"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/typeurl"
	"github.com/golang/protobuf/ptypes/empty"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
)

// Registrator is the concrete type that implements the factory.Registrator and
// proto.OSDServer interfaces.
type Registrator struct {
	Data *userdata.UserData
}

// Register implements the factory.Registrator interface.
func (r *Registrator) Register(s *grpc.Server) {
	proto.RegisterOSDServer(s, r)
}

// Kubeconfig implements the proto.OSDServer interface. The admin kubeconfig is
// generated by kubeadm and placed at /etc/kubernetes/admin.conf. This method
// returns the contents of the generated admin.conf in the response.
func (r *Registrator) Kubeconfig(ctx context.Context, in *empty.Empty) (data *proto.Data, err error) {
	fileBytes, err := ioutil.ReadFile("/etc/kubernetes/admin.conf")
	if err != nil {
		return
	}
	data = &proto.Data{
		Bytes: fileBytes,
	}

	return data, err
}

// Processes implements the proto.OSDServer interface.
// nolint: gocyclo
func (r *Registrator) Processes(ctx context.Context, in *proto.ProcessesRequest) (reply *proto.ProcessesReply, err error) {
	client, err := containerd.New(defaults.DefaultAddress)
	if err != nil {
		return nil, err
	}
	// nolint: errcheck
	defer client.Close()

	ctx = namespaces.WithNamespace(ctx, in.Namespace)

	containers, err := client.Containers(ctx)
	if err != nil {
		return nil, err
	}

	processes := []*proto.Process{}

	for _, container := range containers {
		task, err := container.Task(ctx, nil)
		if err != nil {
			log.Println(err)
			continue
		}

		image, err := container.Image(ctx)
		if err != nil {
			log.Println(err)
			continue
		}

		status, err := task.Status(ctx)
		if err != nil {
			log.Println(err)
			continue
		}

		process := &proto.Process{
			Namespace: in.Namespace,
			Id:        container.ID(),
			Image:     image.Name(),
			Status:    string(status.Status),
		}

		if status.Status == containerd.Running {
			metrics, err := task.Metrics(ctx)
			if err != nil {
				log.Println(err)
				continue
			}

			anydata, err := typeurl.UnmarshalAny(metrics.Data)
			if err != nil {
				log.Println(err)
				continue
			}

			data, ok := anydata.(*cgroups.Metrics)
			if !ok {
				log.Println(errors.New("failed to convert metric data to cgroups.Metrics"))
				continue
			}

			process.MemoryUsage = data.Memory.Usage.Usage
			process.CpuUsage = data.CPU.Usage.Total
		}

		processes = append(processes, process)
	}

	reply = &proto.ProcessesReply{Processes: processes}

	return reply, nil
}

// Restart implements the proto.OSDServer interface.
func (r *Registrator) Restart(ctx context.Context, in *proto.RestartRequest) (reply *proto.RestartReply, err error) {
	ctx = namespaces.WithNamespace(ctx, in.Namespace)
	client, err := containerd.New(defaults.DefaultAddress)
	if err != nil {
		return nil, err
	}
	// nolint: errcheck
	defer client.Close()
	task := client.TaskService()
	_, err = task.Kill(ctx, &tasks.KillRequest{ContainerID: in.Id, Signal: uint32(unix.SIGTERM)})
	if err != nil {
		return nil, err
	}

	reply = &proto.RestartReply{}

	return
}

// Reset implements the proto.OSDServer interface.
func (r *Registrator) Reset(ctx context.Context, in *empty.Empty) (reply *proto.ResetReply, err error) {
	// TODO(andrewrynhard): Delete all system tasks and containers.

	// Set the process arguments.
	args := runner.Args{
		ID:          "reset",
		ProcessArgs: []string{"/bin/kubeadm", "reset", "--force"},
	}

	// Set the mounts.
	// nolint: dupl
	mounts := []specs.Mount{
		{Type: "cgroup", Destination: "/sys/fs/cgroup", Options: []string{"ro"}},
		{Type: "bind", Destination: "/var/run", Source: "/run", Options: []string{"rbind", "rshared", "rw"}},
		{Type: "bind", Destination: "/var/lib/docker", Source: "/var/lib/docker", Options: []string{"rbind", "rshared", "rw"}},
		{Type: "bind", Destination: "/var/lib/kubelet", Source: "/var/lib/kubelet", Options: []string{"rbind", "rshared", "rw"}},
		{Type: "bind", Destination: "/etc/kubernetes", Source: "/etc/kubernetes", Options: []string{"bind", "rw"}},
		{Type: "bind", Destination: "/etc/os-release", Source: "/etc/os-release", Options: []string{"bind", "ro"}},
		{Type: "bind", Destination: "/bin/crictl", Source: "/bin/crictl", Options: []string{"bind", "ro"}},
		{Type: "bind", Destination: "/bin/kubeadm", Source: "/bin/kubeadm", Options: []string{"bind", "ro"}},
	}

	cr := containerdrunner.Containerd{}

	err = cr.Run(
		r.Data,
		args,
		runner.WithContainerImage(constants.KubernetesImage),
		runner.WithOCISpecOpts(
			containerdrunner.WithMemoryLimit(int64(1000000*512)),
			containerdrunner.WithRootfsPropagation("slave"),
			oci.WithMounts(mounts),
			oci.WithHostNamespace(specs.PIDNamespace),
			oci.WithParentCgroupDevices,
			oci.WithPrivileged,
		),
		runner.WithType(runner.Once),
	)

	if err != nil {
		return nil, err
	}

	reply = &proto.ResetReply{}

	return reply, nil
}

// Reboot implements the proto.OSDServer interface.
func (r *Registrator) Reboot(ctx context.Context, in *empty.Empty) (reply *proto.RebootReply, err error) {
	// nolint: errcheck
	unix.Reboot(int(unix.LINUX_REBOOT_CMD_RESTART))

	reply = &proto.RebootReply{}

	return
}

// Dmesg implements the proto.OSDServer interface. The klogctl syscall is used
// to read from the ring buffer at /proc/kmsg by taking the
// SYSLOG_ACTION_READ_ALL action. This action reads all messages remaining in
// the ring buffer non-destructively.
func (r *Registrator) Dmesg(ctx context.Context, in *empty.Empty) (data *proto.Data, err error) {
	// Return the size of the kernel ring buffer
	size, err := unix.Klogctl(constants.SYSLOG_ACTION_SIZE_BUFFER, nil)
	if err != nil {
		return
	}
	// Read all messages from the log (non-destructively)
	buf := make([]byte, size)
	n, err := unix.Klogctl(constants.SYSLOG_ACTION_READ_ALL, buf)
	if err != nil {
		return
	}

	data = &proto.Data{Bytes: buf[:n]}

	return data, err
}

// Logs implements the proto.OSDServer interface. Service or container logs can
// be requested and the contents of the log file are streamed in chunks.
func (r *Registrator) Logs(req *proto.LogsRequest, l proto.OSD_LogsServer) (err error) {
	ctx := namespaces.WithNamespace(context.Background(), req.Namespace)
	client, err := containerd.New(defaults.DefaultAddress)
	if err != nil {
		return err
	}
	// nolint: errcheck
	defer client.Close()
	task, err := client.TaskService().Get(ctx, &tasks.GetRequest{ContainerID: req.Id})
	if err != nil {
		return err
	}
	file, _err := os.OpenFile(task.Process.Stdout, os.O_RDONLY, 0)
	if _err != nil {
		err = _err
		return
	}
	chunk := filechunker.NewChunker(file)

	if chunk == nil {
		return errors.New("no log reader found")
	}

	for data := range chunk.Read(l.Context()) {
		if err = l.Send(&proto.Data{Bytes: data}); err != nil {
			return
		}
	}

	return nil
}

// Version implements the proto.OSDServer interface.
func (r *Registrator) Version(ctx context.Context, in *empty.Empty) (data *proto.Data, err error) {
	v, err := version.NewVersion()
	if err != nil {
		return
	}

	data = &proto.Data{Bytes: []byte(v)}

	return data, err
}