package com.admin.common.task;

import com.admin.common.dto.GostDto;
import com.admin.common.utils.BoundedKeyedExecutor;
import com.admin.common.utils.WebSocketServer;
import com.admin.entity.Forward;
import com.admin.entity.Tunnel;
import com.admin.entity.User;
import com.admin.entity.UserTunnel;
import com.admin.service.ForwardService;
import com.admin.service.TunnelService;
import com.admin.service.UserService;
import com.admin.service.UserTunnelService;
import com.baomidou.mybatisplus.core.conditions.query.QueryWrapper;
import com.baomidou.mybatisplus.core.conditions.update.UpdateWrapper;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

import javax.annotation.PreDestroy;
import javax.annotation.Resource;
import java.util.*;
import java.util.concurrent.TimeUnit;

/** Recheck current limits outside the upload thread and batch pauses by node. */
@Slf4j
@Service
public class FlowLimitEnforcer implements AutoCloseable {
    private static final long BYTES_TO_GB = 1024L * 1024 * 1024;
    private static final int COMMAND_BATCH_SIZE = 128;
    private static final long RUN_BUDGET_NANOS = TimeUnit.SECONDS.toNanos(15);

    @Resource private UserService userService;
    @Resource private UserTunnelService userTunnelService;
    @Resource private ForwardService forwardService;
    @Resource private TunnelService tunnelService;

    private final BoundedKeyedExecutor executor = new BoundedKeyedExecutor("flow-limits", 4, 128,
            error -> log.error("流量限制检查失败", error));

    public boolean schedule(String userId) {
        boolean accepted = executor.submit(userId, () -> enforce(userId));
        if (!accepted) log.warn("流量限制检查队列已满，用户 {} 将在后续上报时重试", userId);
        return accepted;
    }

    void enforce(String userId) {
        long deadline = System.nanoTime() + RUN_BUDGET_NANOS;
        long now = System.currentTimeMillis();
        User user = userService.getById(userId);
        if (user == null) return;
        boolean pauseAll = user.getFlow() * BYTES_TO_GB < user.getInFlow() + user.getOutFlow()
                || (user.getExpTime() != null && user.getExpTime() <= now)
                || !Objects.equals(user.getStatus(), 1);

        Map<Integer, UserTunnel> userTunnels = new HashMap<>();
        for (UserTunnel item : userTunnelService.list(new QueryWrapper<UserTunnel>().eq("user_id", userId))) {
            userTunnels.put(item.getTunnelId(), item);
        }
        List<Forward> forwards = forwardService.list(new QueryWrapper<Forward>()
                .eq("user_id", userId).eq("status", 1));
        Map<Integer, Tunnel> tunnels = new HashMap<>();
        Map<Long, LinkedHashSet<String>> commands = new LinkedHashMap<>();
        Map<Long, Map<Long, List<String>>> targets = new LinkedHashMap<>();
        Map<Long, Forward> snapshots = new HashMap<>();
        for (Forward forward : forwards) {
            UserTunnel userTunnel = userTunnels.get(forward.getTunnelId());
            boolean pauseTunnel = userTunnel != null && (
                    userTunnel.getInFlow() + userTunnel.getOutFlow() >= userTunnel.getFlow() * BYTES_TO_GB
                    || (userTunnel.getExpTime() != null && userTunnel.getExpTime() <= now)
                    || !Objects.equals(userTunnel.getStatus(), 1));
            if (!pauseAll && !pauseTunnel) continue;
            Tunnel tunnel = tunnels.computeIfAbsent(forward.getTunnelId(), tunnelService::getById);
            if (tunnel == null || tunnel.getInNodeId() == null
                    || (Objects.equals(tunnel.getType(), 2) && tunnel.getOutNodeId() == null)) continue;
            String name = forward.getId() + "_" + forward.getUserId() + "_"
                    + (userTunnel == null ? 0 : userTunnel.getId());
            Map<Long, List<String>> required = new LinkedHashMap<>();
            required.computeIfAbsent(tunnel.getInNodeId(), key -> new ArrayList<>()).add(name + "_tcp");
            required.get(tunnel.getInNodeId()).add(name + "_udp");
            if (Objects.equals(tunnel.getType(), 2)) {
                required.computeIfAbsent(tunnel.getOutNodeId(), key -> new ArrayList<>()).add(name + "_tls");
            }
            required.forEach((node, names) -> commands.computeIfAbsent(node, key -> new LinkedHashSet<>()).addAll(names));
            targets.put(forward.getId(), required);
            snapshots.put(forward.getId(), forward);
        }

        Map<Long, Set<String>> acknowledged = new HashMap<>();
        commandLoop:
        for (Map.Entry<Long, LinkedHashSet<String>> command : commands.entrySet()) {
            List<String> names = new ArrayList<>(command.getValue());
            for (int start = 0; start < names.size(); start += COMMAND_BATCH_SIZE) {
                long remaining = TimeUnit.NANOSECONDS.toMillis(deadline - System.nanoTime());
                if (remaining <= 0 || Thread.currentThread().isInterrupted()) break commandLoop;
                List<String> batch = names.subList(start, Math.min(start + COMMAND_BATCH_SIZE, names.size()));
                GostDto response = sendPause(command.getKey(), batch, Math.min(10000, remaining));
                if (response != null && "OK".equals(response.getMsg())) {
                    acknowledged.computeIfAbsent(command.getKey(), key -> new HashSet<>()).addAll(batch);
                } else {
                    log.warn("暂停节点 {} 服务失败，保留状态供后续重试: {}", command.getKey(),
                            response == null ? "无响应" : response.getMsg());
                    break;
                }
            }
        }
        for (Map.Entry<Long, Map<Long, List<String>>> target : targets.entrySet()) {
            boolean complete = target.getValue().entrySet().stream().allMatch(entry ->
                    acknowledged.getOrDefault(entry.getKey(), Collections.emptySet()).containsAll(entry.getValue()));
            if (complete) {
                // A stale entity must not overwrite concurrently uploaded traffic.
                Forward snapshot = snapshots.get(target.getKey());
                UpdateWrapper<Forward> update = new UpdateWrapper<Forward>().eq("id", target.getKey())
                        .eq("status", 1).eq("tunnel_id", snapshot.getTunnelId()).set("status", 0);
                if (snapshot.getUpdatedTime() == null) update.isNull("updated_time");
                else update.eq("updated_time", snapshot.getUpdatedTime());
                forwardService.update(null, update);
            }
        }
    }

    GostDto sendPause(Long nodeId, List<String> names, long timeoutMillis) {
        return WebSocketServer.send_msg(nodeId, Collections.singletonMap("services", names),
                "PauseService", timeoutMillis);
    }

    @Override
    @PreDestroy
    public void close() {
        executor.close();
    }
}
