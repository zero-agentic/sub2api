//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGroup_GetImagePrice_1K 测试 1K 尺寸返回正确价格
func TestGroup_GetImagePrice_1K(t *testing.T) {
	price := 0.10
	group := &Group{
		ImagePrice1K: &price,
	}

	result := group.GetImagePrice("1K")
	require.NotNil(t, result)
	require.InDelta(t, 0.10, *result, 0.0001)
}

// TestGroup_GetImagePrice_2K 测试 2K 尺寸返回正确价格
func TestGroup_GetImagePrice_2K(t *testing.T) {
	price := 0.15
	group := &Group{
		ImagePrice2K: &price,
	}

	result := group.GetImagePrice("2K")
	require.NotNil(t, result)
	require.InDelta(t, 0.15, *result, 0.0001)
}

// TestGroup_GetImagePrice_4K 测试 4K 尺寸返回正确价格
func TestGroup_GetImagePrice_4K(t *testing.T) {
	price := 0.30
	group := &Group{
		ImagePrice4K: &price,
	}

	result := group.GetImagePrice("4K")
	require.NotNil(t, result)
	require.InDelta(t, 0.30, *result, 0.0001)
}

// TestGroup_GetImagePrice_NormalizedSize 测试尺寸归一化计费
func TestGroup_GetImagePrice_NormalizedSize(t *testing.T) {
	price2K := 0.15
	price4K := 0.30
	group := &Group{
		ImagePrice2K: &price2K,
		ImagePrice4K: &price4K,
	}

	// 3K 没有独立价格字段，按下一档 4K 价格计费。
	result := group.GetImagePrice("3K")
	require.NotNil(t, result)
	require.InDelta(t, 0.30, *result, 0.0001)

	// 明确尺寸同样按最长边归档。
	result = group.GetImagePrice("3072x2048")
	require.NotNil(t, result)
	require.InDelta(t, 0.30, *result, 0.0001)

	// 空字符串和未知值仍回退到 2K。
	result = group.GetImagePrice("")
	require.NotNil(t, result)
	require.InDelta(t, 0.15, *result, 0.0001)
	require.Equal(t, result, group.GetImagePrice("unknown"))
}

// TestGroup_GetImagePrice_NilValues 测试未配置时返回 nil
func TestGroup_GetImagePrice_NilValues(t *testing.T) {
	group := &Group{
		// 所有 ImagePrice 字段都是 nil
	}

	require.Nil(t, group.GetImagePrice("1K"))
	require.Nil(t, group.GetImagePrice("2K"))
	require.Nil(t, group.GetImagePrice("4K"))
	require.Nil(t, group.GetImagePrice("unknown"))
}

// TestGroup_GetVideoPrice_NormalizedResolution 测试 resolution 归一化计费，
// 锁死 preflight（apiKeyHasConfiguredVideoPrice）与计费侧（先归一化）依据同一档位。
func TestGroup_GetVideoPrice_NormalizedResolution(t *testing.T) {
	price480P := 0.05
	price4K := 0.50
	group := &Group{
		VideoPrice480P: &price480P,
		VideoPrice4K:   &price4K,
	}

	// 4K 别名按 4K 价格计费，而不是落入默认的 480P 档。
	for _, alias := range []string{"4k", "2160p", "uhd", "2160"} {
		result := group.GetVideoPrice(alias)
		require.NotNil(t, result, "alias %q", alias)
		require.InDelta(t, 0.50, *result, 0.0001, "alias %q", alias)
	}

	// 空字符串和未知值仍回退到 480P。
	result := group.GetVideoPrice("")
	require.NotNil(t, result)
	require.InDelta(t, 0.05, *result, 0.0001)
	require.Equal(t, result, group.GetVideoPrice("unknown"))
}

// TestGroup_GetImagePrice_PartialConfig 测试部分配置
func TestGroup_GetImagePrice_PartialConfig(t *testing.T) {
	price1K := 0.10
	group := &Group{
		ImagePrice1K: &price1K,
		// ImagePrice2K 和 ImagePrice4K 未配置
	}

	result := group.GetImagePrice("1K")
	require.NotNil(t, result)
	require.InDelta(t, 0.10, *result, 0.0001)

	// 2K 和 4K 返回 nil
	require.Nil(t, group.GetImagePrice("2K"))
	require.Nil(t, group.GetImagePrice("4K"))
}
