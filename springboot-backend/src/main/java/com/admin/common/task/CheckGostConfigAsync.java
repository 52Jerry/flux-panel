package com.admin.common.task;

import com.admin.common.dto.*;
import com.admin.common.lang.R;
import com.admin.common.utils.GostUtil;
import com.admin.common.utils.BoundedKeyedExecutor;
import com.admin.common.utils.WebSocketServer;
import com.admin.entity.*;
import com.admin.service.*;
import com.alibaba.fastjson.JSONObject;
import com.baomidou.mybatisplus.core.conditions.query.QueryWrapper;
import lombok.extern.slf4j.Slf4j;
import org.springframework.context.annotation.Lazy;
import org.springframework.stereotype.Service;

import javax.annotation.Resource;
import javax.annotation.PreDestroy;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.concurrent.TimeUnit;

@Slf4j
@Service
public class CheckGostConfigAsync {

    private final BoundedKeyedExecutor cleanupExecutor = new BoundedKeyedExecutor("node-cleanup", 4, 64,
            error -> log.error("节点配置清理失败", error));

    @Resource
    private NodeService nodeService;

    @Resource
    @Lazy
    private ForwardService forwardService;

    @Resource
    @Lazy
    private SpeedLimitService speedLimitService;

    @Resource
    @Lazy
    private TunnelService tunnelService;



    /**
     * 清理孤立的Gost配置项
     */
    public void cleanNodeConfigs(String node_id, GostConfigDto gostConfig) {
        if (node_id == null || gostConfig == null) return;
        if (!cleanupExecutor.submit(node_id, () -> cleanConfigs(node_id, gostConfig))) {
            log.warn("节点配置清理队列已满，节点 {} 将在下次配置上报时重试", node_id);
        }
    }

    private void cleanConfigs(String node_id, GostConfigDto gostConfig) {
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(15);
        Node node = nodeService.getById(node_id);
        if (node != null) {
            cleanOrphanedServices(gostConfig, node, deadline);
            cleanOrphanedChains(gostConfig, node, deadline);
            cleanOrphanedLimiters(gostConfig, node, deadline);
        }
    }

    @PreDestroy
    public void close() {
        cleanupExecutor.close();
    }

    /**
     * 清理孤立的服务
     */
    private void cleanOrphanedServices(GostConfigDto gostConfig, Node node, long deadline) {
        if (gostConfig.getServices() == null) {
            return;
        }

        for (ConfigItem service : gostConfig.getServices()) {
            if (System.nanoTime() >= deadline || Thread.currentThread().isInterrupted()) return;
            safeExecute(() -> {

                if (!Objects.equals(service.getName(), "web_api")){
                    String[] serviceIds = parseServiceName(service.getName());
                    if (serviceIds.length == 4) {
                        String forwardId = serviceIds[0];
                        String userId = serviceIds[1];
                        String userTunnelId = serviceIds[2];
                        String type = serviceIds[3];

                        if (Objects.equals(type, "tcp")) { // 只处理TCP，避免重复处理
                            Forward forward = forwardService.getById(forwardId);
                            if (forward == null) {
                                log.info("删除孤立的服务: {} (节点: {})", service.getName(), node.getId());
                                String name = forwardId + "_" + userId + "_" + userTunnelId;
                                sendCleanup(node.getId(), Map.of("services", List.of(name + "_tcp", name + "_udp")),
                                        "DeleteService", deadline);
                            }
                        }


                        if (Objects.equals(type, "tls")) {
                            Forward forward = forwardService.getById(forwardId);
                            if (forward == null) {
                                log.info("删除孤立的服务: {} (节点: {})", service.getName(), node.getId());
                                sendCleanup(node.getId(), Map.of("services", List.of(service.getName())),
                                        "DeleteService", deadline);
                            }
                        }

                    }
                }


            }, "清理服务 " + service.getName());
        }

    }

    /**
     * 清理孤立的链
     */
    private void cleanOrphanedChains(GostConfigDto gostConfig, Node node, long deadline) {
        if (gostConfig.getChains() == null) {
            return;
        }
        

        for (ConfigItem chain : gostConfig.getChains()) {
            if (System.nanoTime() >= deadline || Thread.currentThread().isInterrupted()) return;
            safeExecute(() -> {
                String[] serviceIds = parseServiceName(chain.getName());
                if (serviceIds.length == 4) {
                    String forwardId = serviceIds[0];
                    String userId = serviceIds[1];
                    String userTunnelId = serviceIds[2];
                    String type = serviceIds[3];
                    
                    if (Objects.equals(type, "chains")) {
                        Forward forward = forwardService.getById(forwardId);
                        if (forward == null) {
                            log.info("删除孤立的链: {} (节点: {})", chain.getName(), node.getId());
                            sendCleanup(node.getId(), Map.of("chain", chain.getName()), "DeleteChains", deadline);
                        }
                    }
                }
            }, "清理链 " + chain.getName());
        }
    }

    /**
     * 清理孤立的限流器
     */
    private void cleanOrphanedLimiters(GostConfigDto gostConfig, Node node, long deadline) {
        if (gostConfig.getLimiters() == null) {
            return;
        }
        

        for (ConfigItem limiter : gostConfig.getLimiters()) {
            if (System.nanoTime() >= deadline || Thread.currentThread().isInterrupted()) return;
            safeExecute(() -> {
                SpeedLimit speedLimit = speedLimitService.getById(limiter.getName());
                if (speedLimit == null) {
                    log.info("删除孤立的限流器: {} (节点: {})", limiter.getName(), node.getId());
                    sendCleanup(node.getId(), Map.of("limiter", limiter.getName()), "DeleteLimiters", deadline);
                }
            }, "清理限流器 " + limiter.getName());
        }
    }

    private void sendCleanup(Long nodeId, Object data, String type, long deadline) {
        long remaining = TimeUnit.NANOSECONDS.toMillis(deadline - System.nanoTime());
        if (remaining <= 0 || Thread.currentThread().isInterrupted()) return;
        GostDto response = WebSocketServer.send_msg(nodeId, data, type, Math.min(10000, remaining));
        if (!"OK".equals(response.getMsg())) {
            log.warn("清理节点 {} 的 {} 失败: {}", nodeId, type, response.getMsg());
        }
    }

    /**
     * 同步限流器
     */
    private void syncLimiters(GostConfigDto gostConfig, Node node) {
        List<Tunnel> tunnelList = tunnelService.list(new QueryWrapper<Tunnel>().eq("in_node_id", node.getId()));
        if (tunnelList == null || tunnelList.isEmpty()) return;
        safeExecute(() -> {
            StringBuilder tunnelIds = new StringBuilder();
            for (Tunnel tunnel : tunnelList) {
                tunnelIds.append(tunnel.getId()).append(",");
            }
            String ids = tunnelIds.deleteCharAt(tunnelIds.length() - 1).toString();
            List<SpeedLimit> speedLimits = speedLimitService.list(new QueryWrapper<SpeedLimit>().in("tunnel_id", ids));
            if (speedLimits != null && !speedLimits.isEmpty()) {
                List<ConfigItem> limiters = gostConfig.getLimiters();
                List<Long> limiters_ids = new ArrayList<>();
                List<Long>  speedLimits_ids = new ArrayList<>();
                if (limiters != null){
                    for (ConfigItem limiter : limiters) {
                        limiters_ids.add(Long.valueOf(limiter.getName()));
                    }
                }
                for (SpeedLimit speedLimit : speedLimits) {
                    speedLimits_ids.add(speedLimit.getId());
                }
                List<Long> diff = new ArrayList<>(speedLimits_ids);
                diff.removeAll(limiters_ids);
                System.out.println(diff);
                if (!diff.isEmpty()) {

                    for (Long speed_id : diff) {
                        SpeedLimit speedLimit = speedLimitService.getById(speed_id);
                        if (speedLimit != null) {
                            SpeedLimitUpdateDto speedLimitUpdateDto = new SpeedLimitUpdateDto();
                            speedLimitUpdateDto.setId(speed_id);
                            speedLimitUpdateDto.setName(speedLimit.getName());
                            speedLimitUpdateDto.setSpeed(speedLimit.getSpeed());
                            speedLimitUpdateDto.setTunnelId(speedLimit.getTunnelId());
                            speedLimitUpdateDto.setTunnelName(speedLimit.getTunnelName());
                            speedLimitService.updateSpeedLimit(speedLimitUpdateDto);
                        }
                    }
                }
            }
        }, "同步限流器 ");
    }

    /**
     * 安全执行操作，捕获异常
     */
    private void safeExecute(Runnable operation, String operationDesc) {
        try {
            operation.run();
        } catch (Exception e) {
            log.info("执行操作失败: {}", operationDesc, e);
        }
    }


    /**
     * 解析服务名称
     */
    private String[] parseServiceName(String serviceName) {
        return serviceName.split("_");
    }
}
