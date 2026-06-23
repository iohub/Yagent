// seq.go - 全局 ID 生成器
package messaging

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
)

// SeqNum 序列号类型
type SeqNum uint64

// IDGen 生成全局唯一 ID
type IDGen struct {
	nodeID  string
	counter atomic.Uint64
}

func NewIDGen(nodeID string) *IDGen {
	return &IDGen{nodeID: nodeID}
}

func (g *IDGen) NextID() string {
	n := g.counter.Add(1)
	return fmt.Sprintf("%s-%x", g.nodeID, n)
}

func (g *IDGen) NextTraceID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
