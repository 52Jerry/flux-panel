package com.admin.common.utils;


import com.admin.common.dto.GostDto;
import com.admin.config.EncryptionConfig;
import com.admin.entity.Node;
import com.admin.service.NodeService;
import com.alibaba.fastjson.JSON;
import com.alibaba.fastjson.JSONObject;
import com.baomidou.mybatisplus.core.conditions.query.QueryWrapper;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.web.socket.CloseStatus;
import org.springframework.web.socket.TextMessage;
import org.springframework.web.socket.WebSocketSession;
import org.springframework.web.socket.handler.TextWebSocketHandler;

import javax.annotation.Resource;
import javax.annotation.PreDestroy;
import javax.annotation.PostConstruct;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.Objects;
import java.util.List;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CopyOnWriteArraySet;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.Semaphore;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.atomic.AtomicLong;
import java.util.UUID;


@Slf4j
public class WebSocketServer extends TextWebSocketHandler {

    @Resource
    NodeService nodeService;

    // 存储所有活跃的 WebSocket 连接（
    private static final CopyOnWriteArraySet<WebSocketSession> activeSessions = new CopyOnWriteArraySet<>();
    
    // 存储节点ID和对应的WebSocket session映射
    private static final ConcurrentHashMap<Long, WebSocketSession> nodeSessions = new ConcurrentHashMap<>();
    
    private static final int MAX_PENDING_REQUESTS = 512;
    private static final Semaphore pendingSlots = new Semaphore(MAX_PENDING_REQUESTS);
    private static final ConcurrentHashMap<String, PendingRequest> pendingRequests = new ConcurrentHashMap<>();
    private static final BoundedSessionSender sender = new BoundedSessionSender(WebSocketServer::cleanupSession);
    private static final String OWNER_ATTRIBUTE = WebSocketServer.class.getName() + ".owner";
    private final AtomicLong statusRepairVersion = new AtomicLong();
    private long repairedVersion;
    private long repairScanVersion;
    private long repairCursor;
    private final ScheduledExecutorService statusRepair = Executors.newSingleThreadScheduledExecutor(task -> {
        Thread thread = new Thread(task, "flux-node-status-repair");
        thread.setDaemon(true);
        return thread;
    });
    final BoundedKeyedExecutor statusUpdates = new BoundedKeyedExecutor("flux-node-status", 2, 2048,
            error -> {
                statusRepairVersion.incrementAndGet();
                log.error("节点状态同步失败，将自动重试", error);
            });

    private static final class PendingRequest {
        final WebSocketSession session;
        final CompletableFuture<GostDto> future = new CompletableFuture<>();
        PendingRequest(WebSocketSession session) { this.session = session; }
    }

    /**
     * 加密消息包装器
     */
    public static class EncryptedMessage {
        private boolean encrypted;
        private String data;
        private Long timestamp;

        // getters and setters
        public boolean isEncrypted() { return encrypted; }
        public void setEncrypted(boolean encrypted) { this.encrypted = encrypted; }
        public String getData() { return data; }
        public void setData(String data) { this.data = data; }
        public Long getTimestamp() { return timestamp; }
        public void setTimestamp(Long timestamp) { this.timestamp = timestamp; }
    }

    //接受客户端消息
    @Override
    public void handleTextMessage(WebSocketSession session, TextMessage message) {
        try {
            if (StringUtils.isNoneBlank(message.getPayload())) {
                
                String id = session.getAttributes().get("id").toString();
                String type = session.getAttributes().get("type").toString();
                String nodeSecret = (String) session.getAttributes().get("nodeSecret");

                // 尝试解密消息
                String decryptedPayload = decryptMessageIfNeeded(message.getPayload(), nodeSecret);

                if (decryptedPayload.contains("memory_usage")){
                    // 先发送确认消息
                    sendToUser(session, "{\"type\":\"call\"}", nodeSecret);
                }else if (decryptedPayload.contains("requestId")) {
                    log.info("收到消息: {}", decryptedPayload);
                    // 处理命令响应消息
                    try {
                        JSONObject responseJson = JSONObject.parseObject(decryptedPayload);
                        String requestId = responseJson.getString("requestId");
                        String responseMessage = responseJson.getString("message");
                        String responseType = responseJson.getString("type");
                        JSONObject responseData = responseJson.getJSONObject("data");
                        
                        if (requestId != null) {
                            PendingRequest request = pendingRequests.get(requestId);

                            if (request != null && request.session == session) {
                                GostDto result = new GostDto();
                                
                                // 根据响应类型处理不同的数据
                                if ("PingResponse".equals(responseType) && responseData != null) {
                                    // 特殊处理ping响应，将完整的响应数据返回
                                    result.setMsg(responseMessage != null ? responseMessage : "OK");
                                    result.setData(responseData); // 保存ping详细结果
                                } else {
                                    // 其他类型的响应
                                    result.setMsg(responseMessage != null ? responseMessage : "无响应消息");
                                    if (responseData != null) {
                                        result.setData(responseData);
                                    }
                                }
                                
                                request.future.complete(result);
                            }
                        }
                    } catch (Exception e) {
                        log.info("处理响应消息失败: {}", e.getMessage(), e);
                    }
                } else {
                    log.info("收到消息: {}", decryptedPayload);
                }

                // 如果是节点类型，转发消息给其他会话
                if (Objects.equals(type, "1")) {
                    JSONObject jsonObject = new JSONObject();
                    jsonObject.put("id", id);
                    jsonObject.put("type", "info");
                    jsonObject.put("data", decryptedPayload);
                    String broadcastMessage = jsonObject.toJSONString();
                    
                    // 异步处理广播消息，避免阻塞当前线程
                    for (WebSocketSession targetSession : activeSessions) {
                        if (targetSession != null && targetSession.isOpen() && !targetSession.equals(session)) {
                            sendToUser(targetSession, broadcastMessage, null);
                        }
                    }
                }
            }
        } catch (Exception e) {
            log.info("处理WebSocket消息时发生异常: {}", e.getMessage(), e);
        }
    }

    /**
     * 尝试解密消息（如果需要）
     */
    private String decryptMessageIfNeeded(String payload, String nodeSecret) {
        if (payload == null || payload.trim().isEmpty()) {
            return payload;
        }

        try {
            // 尝试解析为加密消息格式
            EncryptedMessage encryptedMessage = JSON.parseObject(payload, EncryptedMessage.class);
            
            if (encryptedMessage.isEncrypted() && encryptedMessage.getData() != null) {
                // 获取或创建加密器
                AESCrypto crypto = getOrCreateCrypto(nodeSecret);
                if (crypto == null) {
                    log.info("⚠️ 收到加密消息但无法创建解密器，使用原始数据");
                    return payload;
                }
                
                // 解密数据
                String decryptedData = crypto.decryptString(encryptedMessage.getData());
                return decryptedData;
            }
        } catch (Exception e) {
            // 解析失败，可能是非加密格式，直接返回原始数据
            log.info("WebSocket消息未加密或解密失败，使用原始数据: {}", e.getMessage());
        }
        
        return payload;
    }

    /**
     * 加密消息（如果可能）
     */
    private static String encryptMessageIfPossible(String message, String nodeSecret) {
        if (message == null || nodeSecret == null) {
            return message;
        }

        try {
            AESCrypto crypto = getOrCreateCrypto(nodeSecret);
            if (crypto != null) {
                String encryptedData = crypto.encrypt(message);
                
                // 创建加密消息包装器
                JSONObject encryptedMessage = new JSONObject();
                encryptedMessage.put("encrypted", true);
                encryptedMessage.put("data", encryptedData);
                encryptedMessage.put("timestamp", System.currentTimeMillis());
                
                return encryptedMessage.toJSONString();
            }
        } catch (Exception e) {
            log.info("⚠️ WebSocket消息加密失败，发送原始数据: {}", e.getMessage());
        }

        return message;
    }

    /**
     * 获取或创建加密器实例
     */
    private static AESCrypto getOrCreateCrypto(String secret) {
        return EncryptionConfig.getOrCreateCrypto(secret);
    }

    // Establish and close events share one keyed queue with failure cleanup. Database
    // writes for a node never run concurrently, even when generations overlap.
    @Override
    public void afterConnectionEstablished(WebSocketSession session) {
        try {
            synchronized (session) { session.getAttributes().put(OWNER_ATTRIBUTE, this); }
            String type = session.getAttributes().get("type").toString();
            if (!Objects.equals(type, "1")) {
                activeSessions.add(session);
                return;
            }
            Long nodeId = Long.valueOf(session.getAttributes().get("id").toString());
            WebSocketSession oldSession = nodeSessions.put(nodeId, session);
            scheduleNodeStatus(nodeId);
            if (oldSession != null && oldSession != session) {
                sender.disconnect(oldSession, new IOException("节点已重新连接"));
            }
        } catch (Exception e) {
            log.info("建立连接失败", e);
            sender.disconnect(session, e);
        }
    }

    @Override
    public void afterConnectionClosed(WebSocketSession session, CloseStatus status) {
        cleanupSession(session);
    }

    private void scheduleNodeStatus(Long nodeId) {
        if (!statusUpdates.submit(String.valueOf(nodeId), () -> updateNodeStatus(nodeId))) {
            statusRepairVersion.incrementAndGet();
            log.warn("节点状态队列已满，无法立即同步节点 {}", nodeId);
        }
    }

    @PostConstruct
    public void startStatusRepair() {
        statusRepair.scheduleWithFixedDelay(() -> {
            try { reconcileNodeStatuses(); }
            catch (Exception e) { log.warn("重试节点状态同步失败", e); }
        }, 5, 5, TimeUnit.SECONDS);
    }

    /** Overflow uses a paged database scan instead of an unbounded dirty-node set. */
    synchronized void reconcileNodeStatuses() {
        if (repairScanVersion == 0) {
            long version = statusRepairVersion.get();
            if (version == repairedVersion) return;
            repairScanVersion = version;
            repairCursor = 0;
        }
        List<Node> page = nodeService.list(new QueryWrapper<Node>().select("id")
                .gt("id", repairCursor).orderByAsc("id").last("LIMIT 128"));
        if (page.isEmpty()) {
            repairedVersion = repairScanVersion;
            repairScanVersion = 0;
            return;
        }
        for (Node node : page) {
            Long id = node.getId();
            if (!statusUpdates.submit(String.valueOf(id), () -> updateNodeStatus(id))) return;
            repairCursor = id;
        }
    }

    private void updateNodeStatus(Long nodeId) {
        WebSocketSession current = nodeSessions.get(nodeId);
        Node update = new Node();
        update.setId(nodeId);
        update.setStatus(current == null ? 0 : 1);
        if (current != null) {
            Object version = current.getAttributes().get("nodeVersion");
            Object http = current.getAttributes().get("http");
            Object tls = current.getAttributes().get("tls");
            Object socks = current.getAttributes().get("socks");
            if (version != null) update.setVersion(version.toString());
            if (http != null) update.setHttp(Integer.valueOf(http.toString()));
            if (tls != null) update.setTls(Integer.valueOf(tls.toString()));
            if (socks != null) update.setSocks(Integer.valueOf(socks.toString()));
        }
        boolean updated = nodeService.updateById(update);
        if (nodeSessions.get(nodeId) != current) {
            // A reconnect during the database write is reconciled by this same
            // serial queue. Never broadcast the superseded generation's status.
            scheduleNodeStatus(nodeId);
            return;
        }
        if (!updated) {
            if (current != null) sender.disconnect(current, new IOException("节点状态更新失败"));
            return;
        }
        JSONObject notification = new JSONObject();
        notification.put("id", String.valueOf(nodeId));
        notification.put("type", "status");
        notification.put("data", update.getStatus());
        broadcastMessage(notification.toJSONString());
    }

    // 点对点发送消息
    public static void sendToUser(WebSocketSession socketSession, String message) {
        sendToUser(socketSession, message, null);
    }

    // 点对点发送消息（支持加密）
    public static void sendToUser(WebSocketSession socketSession, String message, String nodeSecret) {
        try {
            enqueue(socketSession, message, nodeSecret, System.nanoTime() + TimeUnit.SECONDS.toNanos(10));
        } catch (Exception e) {
            sender.disconnect(socketSession, e);
        }
    }

    private static BoundedSessionSender.Write enqueue(WebSocketSession session, String message,
                                                       String secret, long deadline) {
        // Include the base64/envelope expansion in the per-session memory budget.
        int bytes = message.getBytes(StandardCharsets.UTF_8).length;
        long reserved = secret == null ? bytes : (long) bytes * 2 + 256;
        if (reserved > BoundedSessionSender.MAX_QUEUED_BYTES) {
            throw new IllegalArgumentException("消息超过发送缓冲区限制");
        }
        return sender.submit(session, () -> secret != null && "1".equals(session.getAttributes().get("type"))
                ? encryptMessageIfPossible(message, secret) : message, (int) reserved, deadline);
    }
    
    /**
     * 清理失效的session，自动识别是节点session还是管理员session
     */
    private static void cleanupSession(WebSocketSession session) {
        if (session == null) return;
        
        IOException reason = new IOException("节点连接已断开");
        sender.remove(session, reason);
        activeSessions.remove(session);
        nodeSessions.forEach((nodeId, current) -> {
            if (current == session && nodeSessions.remove(nodeId, session)) {
                Object owner;
                synchronized (session) { owner = session.getAttributes().get(OWNER_ATTRIBUTE); }
                if (owner instanceof WebSocketServer) ((WebSocketServer) owner).scheduleNodeStatus(nodeId);
            }
        });
        pendingRequests.values().forEach(request -> {
            if (request.session == session) request.future.completeExceptionally(reason);
        });
    }

    @Override
    public void handleTransportError(WebSocketSession session, Throwable exception) {
        sender.disconnect(session, exception);
    }

    // 广播消息
    public static void broadcastMessage(String message) {
        for (WebSocketSession session : activeSessions) {
            sendToUser(session, message);
        }
    }



    public static GostDto send_msg(Long node_id, Object msg, String type) {
        return send_msg(node_id, msg, type, 10_000);
    }

    /** timeoutMillis covers executor wait, socket write, and the command response together. */
    public static GostDto send_msg(Long node_id, Object msg, String type, long timeoutMillis) {
        long deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(Math.max(1, Math.min(timeoutMillis, 10_000)));
        WebSocketSession nodeSession = nodeSessions.get(node_id);
        if (nodeSession == null) {
            return failure("节点不在线");
        }
        if (!nodeSession.isOpen()) {
            cleanupSession(nodeSession);
            return failure("节点连接已断开");
        }
        if (!pendingSlots.tryAcquire()) return failure("等待中的节点命令过多，请稍后重试");
        String requestId = UUID.randomUUID().toString();
        PendingRequest request = new PendingRequest(nodeSession);
        pendingRequests.put(requestId, request);
        BoundedSessionSender.Write write = null;
        try {
            JSONObject data = new JSONObject();
            data.put("type", type);
            data.put("data", msg);
            data.put("requestId", requestId);
            write = enqueue(nodeSession, data.toJSONString(),
                    (String) nodeSession.getAttributes().get("nodeSecret"), deadline);
            write.completion.whenComplete((ignored, error) -> {
                if (error != null) request.future.completeExceptionally(error);
            });
            long remaining = deadline - System.nanoTime();
            if (remaining <= 0) throw new TimeoutException();
            return request.future.get(remaining, TimeUnit.NANOSECONDS);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            return failure("等待节点响应被中断");
        } catch (Exception e) {
            Throwable cause = e instanceof ExecutionException ? e.getCause() : e;
            return failure(cause instanceof TimeoutException ? "等待响应超时" : "发送消息失败: " + cause.getMessage());
        } finally {
            pendingRequests.remove(requestId, request);
            pendingSlots.release();
            if (!request.future.isDone() || request.future.isCompletedExceptionally()) {
                sender.cancel(write, new TimeoutException("请求已结束，取消未完成的发送"));
            }
        }
    }

    private static GostDto failure(String message) {
        GostDto result = new GostDto();
        result.setMsg(message);
        return result;
    }

    @PreDestroy
    public void shutdown() {
        pendingRequests.values().forEach(request -> request.future.completeExceptionally(new IOException("服务正在关闭")));
        sender.close();
        statusRepair.shutdownNow();
        statusUpdates.close();
    }
}
