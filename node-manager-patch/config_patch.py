"""
config.py 健康检查配置补丁

将以下内容添加到 /opt/node-manager/config.py 的 MonitoringConfig 和 load_config 中
"""

# === 在 MonitoringConfig dataclass 中添加以下字段 ===

# 粘贴到 config.py 的 MonitoringConfig 类中:

"""
@dataclass
class MonitoringConfig:
    traffic_sample_interval_seconds: float = 2.0
    device_active_window_seconds: float = 60.0

    # --- 健康检查配置 ---
    # 健康检查间隔（秒）。0=禁用。默认 1200（20分钟）
    health_check_interval_seconds: int = 1200
    # 连续失败多少次后标记为死亡。默认 3（即连续60分钟失败）
    health_check_fail_threshold: int = 3
    # TCP 连接测试超时（秒）。默认 5
    health_check_tcp_timeout_seconds: float = 5.0
    # 每批并发检查数量。默认 100
    health_check_batch_size: int = 100
"""

# === 在 load_config 函数中添加以下解析逻辑 ===

# 粘贴到 config.py 的 load_config 函数中，monitoring 部分之后:

"""
    monitoring = data.get("monitoring", {})
    interval = float(monitoring.get(
        "traffic_sample_interval_seconds",
        result.monitoring.traffic_sample_interval_seconds,
    ))
    if interval < 0.5 or interval > 300:
        raise ValueError("monitoring.traffic_sample_interval_seconds must be between 0.5 and 300")
    result.monitoring.traffic_sample_interval_seconds = interval
    device_window = float(monitoring.get(
        "device_active_window_seconds",
        result.monitoring.device_active_window_seconds,
    ))
    if device_window < 1 or device_window > 3600:
        raise ValueError(
            "monitoring.device_active_window_seconds must be between 1 and 3600"
        )
    result.monitoring.device_active_window_seconds = device_window

    # --- 健康检查配置 ---
    result.monitoring.health_check_interval_seconds = int(
        monitoring.get("health_check_interval_seconds", 1200)
    )
    result.monitoring.health_check_fail_threshold = int(
        monitoring.get("health_check_fail_threshold", 3)
    )
    result.monitoring.health_check_tcp_timeout_seconds = float(
        monitoring.get("health_check_tcp_timeout_seconds", 5.0)
    )
    result.monitoring.health_check_batch_size = int(
        monitoring.get("health_check_batch_size", 100)
    )
"""

# === 修改 main.py 的 startup_tasks 和 shutdown_tasks ===

# 在 main.py 的 startup_tasks() 函数末尾添加:
"""
    start_health_checker()
"""

# 在 main.py 的 shutdown_tasks() 函数中添加:
"""
    stop_health_checker()
"""

# 在 main.py 的 import 部分添加:
"""
from monitor.health_check import start_health_checker, stop_health_checker, get_dead_outbounds
"""

# === 在 /etc/node-manager/config.yaml 中添加配置 ===

# 粘贴到 config.yaml 的 monitoring 部分:
"""
monitoring:
  traffic_sample_interval_seconds: 2.0
  device_active_window_seconds: 60.0
  # 健康检查：每20分钟检查所有上游SOCKS代理的TCP可达性
  # 只记录告警日志，不自动删除，需人工确认后手动清理
  health_check_interval_seconds: 1200
  # 连续3次失败才标记为死亡（60分钟）
  health_check_fail_threshold: 3
  health_check_tcp_timeout_seconds: 5.0
  health_check_batch_size: 100
"""
