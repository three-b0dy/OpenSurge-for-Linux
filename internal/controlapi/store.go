package controlapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Dir() string { return s.dir }

func (s *Store) Ensure() error {
	for _, dir := range []string{s.dir, filepath.Join(s.dir, "sources"), filepath.Join(s.dir, "operations")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Recovery() (RecoveryState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state RecoveryState
	err := readJSON(filepath.Join(s.dir, "recovery.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		return RecoveryState{SchemaVersion: SchemaVersion, Stage: RecoveryIdle}, nil
	}
	return state, err
}

func (s *Store) SaveRecovery(state RecoveryState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveRecoveryLocked(state)
}

func (s *Store) saveRecoveryLocked(state RecoveryState) error {
	state.SchemaVersion = SchemaVersion
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "recovery.json"), append(data, '\n'), 0o600)
}

func (s *Store) SaveRecoveryCard(state RecoveryState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.NetworkSnapshot == nil {
		return fmt.Errorf("recovery card requires a network snapshot")
	}
	snapshot := state.NetworkSnapshot
	card := fmt.Sprintf(`OpenSurge for Linux - 同一 LAN DHCP 恢复卡

创建时间：%s
网络服务：%s
接口：%s
原始 IPv4：%s
子网掩码：%s
原始路由器：%s
原始 DNS：%s

推荐恢复顺序：
1. 在浏览器打开原始路由器地址，登录路由器管理后台。
2. 进入 LAN / 网络设置 / DHCP 服务器，重新开启路由器 DHCP 并保存；保留路由器 LAN IP 不变。
3. 确认另一台设备能够从路由器自动获得 IPv4、网关和 DNS。
4. 回到 OpenSurge 执行 OFFER 探测。自动 DHCP 的实际恢复动作由 Linux gateway lifecycle 管理；
   当前 foundation 不会伪造恢复成功，请在 lifecycle 可用后再执行此步骤。
5. 让客户端重新连接该 LAN（Wi-Fi 设备重连 Wi-Fi；有线设备重新获取地址），确认自动获取地址并能访问互联网。

重要：恢复自动获取的路径必须先确认路由器 DHCP 已恢复并通过 OFFER 探测；网络接口的 DHCP
切换仍由 Linux gateway lifecycle 管理。

如果你明确要让网关主机长期保持当前静态 IPv4，可在 OpenSurge 网关停止后使用 Web GUI 的
“保留静态 IP 并结束”跳过路由器 DHCP 探测和自动 DHCP 恢复。此选择不会验证或恢复
其他客户端的自动获取能力；其他设备必须使用有效静态配置，或由另一个 DHCP 服务器提供地址。
`, time.Now().UTC().Format(time.RFC3339), snapshot.NetworkService, snapshot.Interface, snapshot.IPv4, snapshot.SubnetMask, snapshot.Router, strings.Join(snapshot.DNS, ", "))
	return writeAtomic(filepath.Join(s.dir, "WIFI-DHCP-RECOVERY-CARD.txt"), []byte(card), 0o600)
}

func (s *Store) RecoveryCard() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.ReadFile(filepath.Join(s.dir, "WIFI-DHCP-RECOVERY-CARD.txt"))
}

func (s *Store) DiscardPreparedRecovery(topology string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var current RecoveryState
	if err := readJSON(filepath.Join(s.dir, "recovery.json"), &current); err != nil {
		return err
	}
	if current.Stage != RecoveryPrepared {
		return fmt.Errorf("only prepared recovery data can be discarded")
	}
	if err := s.saveRecoveryLocked(RecoveryState{SchemaVersion: SchemaVersion, Stage: RecoveryIdle, Topology: topology}); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.dir, "WIFI-DHCP-RECOVERY-CARD.txt")); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = s.saveRecoveryLocked(current)
		return err
	}
	return nil
}

func (s *Store) SaveOperation(op Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "operations", op.ID+".json"), append(data, '\n'), 0o600)
}

func (s *Store) Operation(id string) (Operation, error) {
	var op Operation
	if id == "" || filepath.Base(id) != id {
		return op, fmt.Errorf("invalid operation id")
	}
	err := readJSON(filepath.Join(s.dir, "operations", id+".json"), &op)
	return op, err
}

func (s *Store) Operations(limit int) ([]Operation, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "operations"))
	if err != nil {
		return nil, err
	}
	operations := make([]Operation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var operation Operation
		if err := readJSON(filepath.Join(s.dir, "operations", entry.Name()), &operation); err == nil {
			operations = append(operations, operation)
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].UpdatedAt.After(operations[j].UpdatedAt) })
	if limit > 0 && len(operations) > limit {
		operations = operations[:limit]
	}
	return operations, nil
}

func (s *Store) Sources() ([]Source, error) {
	var sources []Source
	err := readJSON(filepath.Join(s.dir, "sources.json"), &sources)
	if errors.Is(err, os.ErrNotExist) {
		return []Source{}, nil
	}
	return sources, err
}

func (s *Store) SaveSources(sources []Source) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(sources, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "sources.json"), append(data, '\n'), 0o600)
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".opensurge-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
