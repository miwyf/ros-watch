# IP 归属地数据库

来源：[ip2region v3.18.0](https://github.com/lionsoul2014/ip2region/tree/v3.18.0)。固定提交：`c1a1fc7d5941760db3f8431dc05c48cf7f0e30a1`，版本发布于 2026-09-08。使用上游 Apache-2.0 OR MIT 授权，完整许可保存在 LICENSE-ip2region.md。

| 文件 | 字节数 | SHA256 |
| --- | ---: | --- |
| ip2region_v4.xdb | 11122036 | 8e31bbdccb5bf21028af10592d4312ec975da0bffa108c0c5d862a12190f9ad3 |
| ip2region_v6.xdb | 37277813 | 939f6b46bd2b8bec3cf7c5ceb8ba782266ae9b1f35b5ba7916700dec0b7506ed |

保留原始数据库，无数据修改。运行时使用只读文件和约 1 MiB 的双栈索引；不向网络查询来源 IP。地区为数据库所记录的网络出口归属，可能过时或不准确，不能推断个人身份或精确物理位置。

更新时从上游指定版本替换两个 xdb 文件，核对版本和 SHA256，更新本文件及 geolocation.go 的 geoDataVersion，再测试、构建镜像。新镜像会为历史记录即时显示新库的结果；日志库不存储或迁移归属地字段。
