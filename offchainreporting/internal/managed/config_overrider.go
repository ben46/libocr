package managed

import "github.com/smartcontractkit/libocr/offchainreporting/types"

// ConfigOverriderWrapper 结构体实现了 types.ConfigOverrider 接口
var _ types.ConfigOverrider = ConfigOverriderWrapper{}

// ConfigOverriderWrapper 结构体是对 types.ConfigOverrider 的包装，能够优雅地处理 nil ConfigOverrider
type ConfigOverriderWrapper struct {
	wrapped types.ConfigOverrider
}

// ConfigOverride 方法返回 ConfigOverrider 包装的 ConfigOverride
func (cow ConfigOverriderWrapper) ConfigOverride() *types.ConfigOverride {
	if cow.wrapped == nil {
		return nil
	}
	return cow.wrapped.ConfigOverride()
}
