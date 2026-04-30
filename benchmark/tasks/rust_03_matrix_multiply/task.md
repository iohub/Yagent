# 任务：实现并行矩阵乘法

请实现一个支持并行计算的矩阵乘法库 `Matrix`。

## 功能要求

1. **Matrix 结构体** — 存储 rows×cols 的 f64 矩阵
2. **new(rows, cols)** — 创建全零矩阵
3. **from_vec(rows, cols, data)** — 从向量创建矩阵（行优先）
4. **get/set** — 访问和修改元素
5. **multiply(&Matrix) -> Matrix** — 并行矩阵乘法（使用 rayon 或多线程）
6. **multiply_seq(&Matrix) -> Matrix** — 串行矩阵乘法（用于对比验证）
7. **add(&Matrix) -> Matrix** — 矩阵加法
8. **transpose() -> Matrix** — 矩阵转置

## 技术约束

- 并行乘法可使用 `rayon` crate（已添加依赖）或 `std::thread` 手动实现
- 必须验证矩阵维度兼容性（A 的列数 == B 的行数），不匹配时返回错误
- 小矩阵（如 32x32 以下）可自动回退到串行算法

## 验收标准

运行 `cargo test` 所有测试通过即完成任务。
