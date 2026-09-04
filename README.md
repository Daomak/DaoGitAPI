# DaoGitAPI

一站式 GitHub / Gitee 仓库管理桌面工具，通过 API 操作两个平台的所有仓库功能。

## 功能特性

### 仓库管理
- 查看仓库列表，支持搜索过滤
- 创建仓库（公开 / 私有）
- 编辑仓库信息（名称、描述、公开/私有）
- 删除仓库
- 仓库公开状态标识（公开绿色 / 私有红色）

### 文件管理
- 浏览仓库文件目录，支持进入子目录 / 返回上级
- 新建文件、上传文件、上传文件夹（递归上传）
- 删除单个文件、全部删除（递归删除目录下所有文件）
- 实时上传 / 删除进度显示

### 分支管理
- 查看分支列表
- 创建新分支
- 删除分支

### 提交记录
- 查看仓库提交历史
- 显示提交信息、作者、时间

### Issue 管理
- 查看 Issue 列表
- 创建、编辑、关闭 Issue

### Pull Request
- 查看 PR 列表
- 查看 PR 详情

### Release 管理
- 查看 Release 列表
- 创建 Release（自动获取默认分支）
- 编辑 Release 信息
- 删除 Release
- 上传 Release 附件（带进度条）
- 删除 Release 附件（自动获取附件真实 ID）

### 其他特性
- **双平台切换**：支持 GitHub 与 Gitee 两个平台，一键切换
- **Token 本地存储**：API Token 存储在系统用户配置目录（`%APPDATA%\DaoGitAPI`），防止软件复制到其他电脑时 Token 泄漏
- **先选仓库再操作**：未选择仓库时只显示仓库管理和设置，选择后显示对应仓库功能
- **单实例运行**：内置互斥体，防止程序多开
- **UI 弹窗**：所有提示和确认均使用自定义 UI 弹窗，无原生系统弹窗

## 系统要求

- Windows 10 / Windows 11（64 位）
- 网络连接（访问 GitHub / Gitee API）

## Token 配置

1. 打开软件，进入「设置」页面
2. 填写 GitHub Personal Access Token（需勾选 `repo` 权限）
3. 填写 Gitee Private Token（需勾选项目相关权限）
4. Token 仅保存在本地，不会上传到任何服务器

### GitHub Token 获取
- 地址：https://github.com/settings/tokens
- 权限：勾选 `repo`（完整仓库访问权限）

### Gitee Token 获取
- 地址：https://gitee.com/profile/personal_access_tokens
- 权限：勾选项目相关权限

## 技术栈

- **后端**：Go + Wails v2
- **前端**：原生 HTML / CSS / JavaScript
- **窗口**：WebView2
- **编译**：CGO_ENABLED=0，单文件绿色运行

## 关于

- **作者**：Daomak
- **作者官网**：www.daomak.com
- **开源协议**：MIT

## 开源地址

- GitHub：https://github.com/Daomak/DaoGitAPI
- Gitee：https://gitee.com/daomak/daogitapi
