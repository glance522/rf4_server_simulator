# 文档索引

仓库根目录的 [README](../README.md) 是项目入口，本文档目录汇总部署、实现、玩法和开发资料。

## 使用与部署

- [部署指南](deploy.md)：构建、配置、初始化、启动、客户端接入与安全约束。
- [客户端兼容性检查清单](client-checklist.md)：登录、会话、玩家状态、钓鱼、物品与经济等回归项。
- [管理控制台](admin-console.md)：launcher 内嵌 Control Deck 的功能与 API。
- [静态数据隔离](static_runtime_isolation.md)：可写玩家库与只读静态目录的运行时隔离模型。

## 架构与实现

- [架构与实现概览](architecture.md)：组件关系、当前主链路、目录结构与已知边界。
- [钓鱼机制](gameplay.md)：鱼群分布、吃饵偏好、天气鱼口、饵料消耗和实现边界。
- [商店目录](shop_catalog.md)：货架解析顺序、覆盖规则与支持范围。
- [个人中心](portal.md)：角色资料、余额、会员购买与身份边界。
- [咖啡厅订单](cafe_orders.md)：订单生成、交单事务与迁移兼容说明。

## 开发与协议

- [调试工作流](debugging-workflow.md)：本地调试、日志和协议排查流程。
- [官方协议工作坊](official-protocol-workshop.md)：协议观察、记录和整理资料。
