# Frontend Development Guidelines

> Best practices for frontend development in this project.

---

## Overview

单仓库模式：前端源码在 `web/`（Vue 3 + Vite + TypeScript + Tailwind），构建产物
输出到 `internal/webui/dist` 由 Go `go:embed` 嵌入，运行时单进程。面板 UI 语言为
简体中文；视觉风格为「精密仪表」暗色系（深蓝黑基底 + 琥珀高亮 + 青色数据）。

---

## Guidelines Index

| Guide | Description | Status |
|-------|-------------|--------|
| [Directory Structure](./directory-structure.md) | web/ 目录布局与构建产物约束 | 已填充 |
| [Component Guidelines](./component-guidelines.md) | 组件模式、弹窗、Teleport | 已填充 |
| [Hook Guidelines](./hook-guidelines.md) | composables：轮询、主题、Toast | 已填充 |
| [State Management](./state-management.md) | 无 Pinia；reactive 单例 + 组件局部状态 | 已填充 |
| [Quality Guidelines](./quality-guidelines.md) | vue-tsc 门禁、契约对齐、明文 Key 红线 | 已填充 |
| [Type Safety](./type-safety.md) | types.ts 与后端 JSON 契约一一对应 | 已填充 |

---

## How to Fill These Guidelines

For each guideline file:

1. Document your project's **actual conventions** (not ideals)
2. Include **code examples** from your codebase
3. List **forbidden patterns** and why
4. Add **common mistakes** your team has made

The goal is to help AI assistants and new team members understand how YOUR project works.

---

**Language**: 本仓库说明文档用中文（与既有 backend spec 一致）。
