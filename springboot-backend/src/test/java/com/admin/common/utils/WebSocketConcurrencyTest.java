package com.admin.common.utils;

import com.admin.common.dto.GostDto;
import com.admin.entity.Node;
import com.admin.service.NodeService;
import com.alibaba.fastjson.JSON;
import org.junit.jupiter.api.Test;
import org.springframework.web.socket.TextMessage;
import org.springframework.web.socket.CloseStatus;
import org.springframework.web.socket.WebSocketSession;

import java.lang.reflect.Field;
import java.lang.reflect.Proxy;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Consumer;

import static org.junit.jupiter.api.Assertions.*;

class WebSocketConcurrencyTest {
    private static final AtomicInteger ids = new AtomicInteger(10000);

    private static final class Peer {
        final long id = ids.incrementAndGet();
        final AtomicBoolean open = new AtomicBoolean(true);
        final Map<String, Object> attributes = new ConcurrentHashMap<>();
        final WebSocketSession session;
        volatile Consumer<TextMessage> send = message -> {};
        volatile Runnable close = () -> {};

        Peer(boolean node) {
            attributes.put("id", String.valueOf(id));
            attributes.put("type", node ? "1" : "0");
            session = (WebSocketSession) Proxy.newProxyInstance(getClass().getClassLoader(),
                    new Class<?>[]{WebSocketSession.class}, (proxy, method, args) -> {
                        switch (method.getName()) {
                            case "getId": return String.valueOf(id);
                            case "getAttributes": return attributes;
                            case "isOpen": return open.get();
                            case "sendMessage": send.accept((TextMessage) args[0]); return null;
                            case "close": open.set(false); close.run(); return null;
                            case "equals": return proxy == args[0];
                            case "hashCode": return System.identityHashCode(proxy);
                            case "toString": return "TestPeer(" + id + ")";
                            default: return null;
                        }
                    });
        }
    }

    @SuppressWarnings("unchecked")
    private static <T> T field(String name) throws Exception {
        Field field = WebSocketServer.class.getDeclaredField(name);
        field.setAccessible(true);
        return (T) field.get(null);
    }

    private static Peer node() throws Exception {
        Peer peer = new Peer(true);
        WebSocketConcurrencyTest.<Map<Long, WebSocketSession>>field("nodeSessions").put(peer.id, peer.session);
        return peer;
    }

    private static void await(CountDownLatch latch) {
        try { assertTrue(latch.await(5, TimeUnit.SECONDS), "test latch timeout"); }
        catch (InterruptedException e) { Thread.currentThread().interrupt(); }
    }

    private static void disconnect(Peer peer) {
        new WebSocketServer().handleTransportError(peer.session, new IllegalStateException("test closed"));
    }

    private static WebSocketServer handler(Consumer<Node> update) {
        WebSocketServer handler = new WebSocketServer();
        handler.nodeService = (NodeService) Proxy.newProxyInstance(WebSocketConcurrencyTest.class.getClassLoader(),
                new Class<?>[]{NodeService.class}, (proxy, method, args) -> {
                    if (method.getName().equals("updateById")) { update.accept((Node) args[0]); return true; }
                    throw new UnsupportedOperationException(method.getName());
                });
        return handler;
    }

    @Test
    void slowAdministratorDoesNotBlockHealthyPeerOrNode() throws Exception {
        CountDownLatch blocked = new CountDownLatch(1), release = new CountDownLatch(1);
        Peer slow = new Peer(false), fast = new Peer(false), node = new Peer(true);
        slow.send = message -> { blocked.countDown(); await(release); };
        try (BoundedSessionSender sender = new BoundedSessionSender(session -> {})) {
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
            sender.submit(slow.session, () -> "slow", 4, deadline);
            assertTrue(blocked.await(1, TimeUnit.SECONDS));
            sender.submit(fast.session, () -> "fast", 4, deadline).completion.get(1, TimeUnit.SECONDS);
            sender.submit(node.session, () -> "node", 4, deadline).completion.get(1, TimeUnit.SECONDS);
        } finally { release.countDown(); }
    }

    @Test
    void allAdministratorWorkersCanBlockWithoutConsumingNodeWorkers() throws Exception {
        CountDownLatch blocked = new CountDownLatch(4), release = new CountDownLatch(1);
        try (BoundedSessionSender sender = new BoundedSessionSender(session -> {})) {
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
            for (int i = 0; i < 4; i++) {
                Peer slow = new Peer(false);
                slow.send = message -> { blocked.countDown(); await(release); };
                sender.submit(slow.session, () -> "slow", 4, deadline);
            }
            assertTrue(blocked.await(1, TimeUnit.SECONDS));
            sender.submit(new Peer(true).session, () -> "node", 4, deadline).completion.get(1, TimeUnit.SECONDS);
        } finally { release.countDown(); }
    }

    @Test
    void writesStayInOrderAndNeverOverlapOnOneSession() throws Exception {
        Peer peer = new Peer(true);
        AtomicInteger inFlight = new AtomicInteger(), maximum = new AtomicInteger();
        List<Integer> delivered = new CopyOnWriteArrayList<>();
        peer.send = message -> {
            maximum.accumulateAndGet(inFlight.incrementAndGet(), Math::max);
            delivered.add(Integer.valueOf(message.getPayload()));
            Thread.yield();
            inFlight.decrementAndGet();
        };
        try (BoundedSessionSender sender = new BoundedSessionSender(session -> {})) {
            List<CompletableFuture<Void>> writes = new ArrayList<>();
            for (int i = 0; i < 100; i++) {
                String message = String.valueOf(i);
                writes.add(sender.submit(peer.session, () -> message, message.length(),
                        System.nanoTime() + TimeUnit.SECONDS.toNanos(5)).completion);
            }
            CompletableFuture.allOf(writes.toArray(new CompletableFuture<?>[0])).get(3, TimeUnit.SECONDS);
            assertEquals(1, maximum.get());
            assertEquals(100, delivered.size());
            for (int i = 0; i < delivered.size(); i++) assertEquals(i, delivered.get(i));
        }
    }

    @Test
    void fullSessionQueueRejectsAndReleasesEveryQueuedWrite() throws Exception {
        Peer peer = new Peer(false);
        CountDownLatch entered = new CountDownLatch(1), release = new CountDownLatch(1);
        peer.send = message -> { entered.countDown(); await(release); };
        peer.close = release::countDown;
        try (BoundedSessionSender sender = new BoundedSessionSender(session -> {})) {
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
            sender.submit(peer.session, () -> "current", 7, deadline);
            assertTrue(entered.await(1, TimeUnit.SECONDS));
            List<BoundedSessionSender.Write> queued = new ArrayList<>();
            for (int i = 0; i < BoundedSessionSender.MAX_MESSAGES; i++) {
                queued.add(sender.submit(peer.session, () -> "queued", 6, deadline));
            }
            assertThrows(RejectedExecutionException.class, () -> sender.submit(peer.session, () -> "overflow", 8, deadline));
            assertTrue(queued.stream().allMatch(write -> write.completion.isCompletedExceptionally()));
            assertTrue(queued.stream().allMatch(write -> write.message == null));
            assertThrows(RejectedExecutionException.class, () -> sender.submit(peer.session, () -> "late", 4, deadline));
        } finally { release.countDown(); }
    }

    @Test
    void queuedCommandExpiresWithoutBeingSentLaterOrClosingHealthyConnection() throws Exception {
        Peer peer = node();
        CountDownLatch entered = new CountDownLatch(1), release = new CountDownLatch(1), delivered = new CountDownLatch(1);
        AtomicInteger commands = new AtomicInteger();
        peer.send = message -> {
            if (message.getPayload().equals("block")) { entered.countDown(); await(release); delivered.countDown(); }
            else commands.incrementAndGet();
        };
        try {
            WebSocketServer.sendToUser(peer.session, "block");
            assertTrue(entered.await(1, TimeUnit.SECONDS));
            GostDto result = WebSocketServer.send_msg(peer.id, Map.of(), "QueuedCommand", 120);
            assertEquals("等待响应超时", result.getMsg());
            release.countDown();
            assertTrue(delivered.await(1, TimeUnit.SECONDS));
            BoundedSessionSender sender = field("sender");
            sender.submit(peer.session, () -> "barrier", 7, System.nanoTime() + TimeUnit.SECONDS.toNanos(1))
                    .completion.get(1, TimeUnit.SECONDS);
            assertEquals(1, commands.get(), "only the barrier is delivered after timeout");
            assertTrue(peer.open.get());
            assertTrue(WebSocketConcurrencyTest.<Map<?, ?>>field("pendingRequests").isEmpty());
        } finally { release.countDown(); disconnect(peer); }
    }

    @Test
    void runningSocketWriteIsIncludedInCommandDeadline() throws Exception {
        Peer peer = node();
        CountDownLatch release = new CountDownLatch(1), closed = new CountDownLatch(1);
        peer.send = message -> await(release);
        peer.close = () -> { release.countDown(); closed.countDown(); };
        try {
            long start = System.nanoTime();
            GostDto result = WebSocketServer.send_msg(peer.id, Map.of(), "SlowWrite", 120);
            assertEquals("等待响应超时", result.getMsg());
            assertTrue(System.nanoTime() - start < TimeUnit.SECONDS.toNanos(1));
            assertTrue(closed.await(1, TimeUnit.SECONDS));
            assertTrue(WebSocketConcurrencyTest.<Map<?, ?>>field("pendingRequests").isEmpty());
        } finally { release.countDown(); disconnect(peer); }
    }

    @Test
    void missingResponseDoesNotCloseSocketAfterSuccessfulWrite() throws Exception {
        Peer peer = node();
        try {
            assertEquals("等待响应超时", WebSocketServer.send_msg(peer.id, Map.of(), "NoReply", 120).getMsg());
            assertTrue(peer.open.get());
        } finally { disconnect(peer); }
    }

    @Test
    void immediateResponseBeforeSendReturnsKeepsConnectionOpen() throws Exception {
        Peer peer = node();
        CountDownLatch release = new CountDownLatch(1);
        peer.send = message -> {
            String requestId = JSON.parseObject(message.getPayload()).getString("requestId");
            new WebSocketServer().handleTextMessage(peer.session,
                    new TextMessage("{\"requestId\":\"" + requestId + "\",\"message\":\"OK\"}"));
            await(release);
        };
        try {
            assertEquals("OK", WebSocketServer.send_msg(peer.id, Map.of(), "Immediate", 1000).getMsg());
            assertTrue(peer.open.get());
        } finally { release.countDown(); disconnect(peer); }
    }

    @Test
    void disconnectReleasesPendingRequestWithoutWaitingForDeadline() throws Exception {
        Peer peer = node();
        CountDownLatch sent = new CountDownLatch(1);
        peer.send = message -> sent.countDown();
        CompletableFuture<GostDto> call = CompletableFuture.supplyAsync(() -> WebSocketServer.send_msg(peer.id, Map.of(), "Disconnect", 5000));
        try {
            assertTrue(sent.await(1, TimeUnit.SECONDS));
            disconnect(peer);
            assertTrue(call.get(1, TimeUnit.SECONDS).getMsg().contains("连接已断开"));
            assertTrue(WebSocketConcurrencyTest.<Map<?, ?>>field("pendingRequests").isEmpty());
        } finally { disconnect(peer); }
    }

    @Test
    void pendingAdmissionLimitFailsImmediatelyWithoutAllocatingRequest() throws Exception {
        Peer peer = node();
        Semaphore permits = field("pendingSlots");
        int held = permits.drainPermits();
        try {
            assertEquals(512, held);
            assertTrue(WebSocketServer.send_msg(peer.id, Map.of(), "Busy").getMsg().contains("命令过多"));
            assertTrue(WebSocketConcurrencyTest.<Map<?, ?>>field("pendingRequests").isEmpty());
        } finally { permits.release(held); disconnect(peer); }
    }

    @Test
    void failedCurrentConnectionMarksNodeOfflineExactlyOnce() throws Exception {
        Peer peer = new Peer(true);
        CountDownLatch online = new CountDownLatch(1), offline = new CountDownLatch(1), closed = new CountDownLatch(1);
        List<Integer> writes = new CopyOnWriteArrayList<>();
        WebSocketServer handler = handler(update -> {
            writes.add(update.getStatus());
            if (update.getStatus() == 1) online.countDown(); else offline.countDown();
        });
        peer.close = () -> { handler.afterConnectionClosed(peer.session, CloseStatus.NORMAL); closed.countDown(); };
        try {
            handler.afterConnectionEstablished(peer.session);
            assertTrue(online.await(1, TimeUnit.SECONDS));
            handler.handleTransportError(peer.session, new IllegalStateException("write failed"));
            assertTrue(offline.await(1, TimeUnit.SECONDS));
            assertTrue(closed.await(1, TimeUnit.SECONDS));
            assertEquals(List.of(1, 0), writes);
            assertFalse(WebSocketConcurrencyTest.<Map<Long, ?>>field("nodeSessions").containsKey(peer.id));
        } finally { disconnect(peer); handler.statusUpdates.close(); }
    }

    @Test
    void oldGenerationCloseCannotMarkReplacementOffline() throws Exception {
        Peer old = new Peer(true), replacement = new Peer(true);
        replacement.attributes.put("id", String.valueOf(old.id));
        CountDownLatch firstOnline = new CountDownLatch(1), secondOnline = new CountDownLatch(1), oldClosed = new CountDownLatch(1);
        List<Integer> writes = new CopyOnWriteArrayList<>();
        WebSocketServer handler = handler(update -> {
            writes.add(update.getStatus());
            if (writes.size() == 1) firstOnline.countDown(); else secondOnline.countDown();
        });
        old.close = () -> { handler.afterConnectionClosed(old.session, CloseStatus.NORMAL); oldClosed.countDown(); };
        try {
            handler.afterConnectionEstablished(old.session);
            assertTrue(firstOnline.await(1, TimeUnit.SECONDS));
            handler.afterConnectionEstablished(replacement.session);
            assertTrue(secondOnline.await(1, TimeUnit.SECONDS));
            assertTrue(oldClosed.await(1, TimeUnit.SECONDS));
            assertEquals(List.of(1, 1), writes);
            assertSame(replacement.session, WebSocketConcurrencyTest.<Map<Long, ?>>field("nodeSessions").get(old.id));
        } finally { disconnect(old); disconnect(replacement); handler.statusUpdates.close(); }
    }

    @Test
    void reconnectDuringOfflineDatabaseWriteReconcilesOnlineWithoutStaleBroadcast() throws Exception {
        Peer old = new Peer(true), replacement = new Peer(true), admin = new Peer(false);
        replacement.attributes.put("id", String.valueOf(old.id));
        CountDownLatch firstOnline = new CountDownLatch(1), writingOffline = new CountDownLatch(1), release = new CountDownLatch(1), reconciled = new CountDownLatch(1);
        AtomicInteger storedStatus = new AtomicInteger(-1), onlineWrites = new AtomicInteger();
        List<Integer> notifications = new CopyOnWriteArrayList<>();
        admin.send = message -> notifications.add(JSON.parseObject(message.getPayload()).getIntValue("data"));
        WebSocketServer handler = handler(update -> {
            if (update.getStatus() == 0) { writingOffline.countDown(); await(release); }
            storedStatus.set(update.getStatus());
            if (update.getStatus() == 1 && onlineWrites.incrementAndGet() == 1) firstOnline.countDown();
            else if (update.getStatus() == 1) reconciled.countDown();
        });
        try {
            handler.afterConnectionEstablished(admin.session);
            handler.afterConnectionEstablished(old.session);
            assertTrue(firstOnline.await(1, TimeUnit.SECONDS));
            handler.handleTransportError(old.session, new IllegalStateException("failed"));
            assertTrue(writingOffline.await(1, TimeUnit.SECONDS));
            handler.afterConnectionEstablished(replacement.session);
            release.countDown();
            assertTrue(reconciled.await(1, TimeUnit.SECONDS));
            assertEquals(1, storedStatus.get());
            assertFalse(notifications.contains(0));
            assertSame(replacement.session, WebSocketConcurrencyTest.<Map<Long, ?>>field("nodeSessions").get(old.id));
        } finally { release.countDown(); disconnect(old); disconnect(replacement); disconnect(admin); handler.statusUpdates.close(); }
    }

    @Test
    void oversizedFireAndForgetMessageClosesAndRemovesSocket() throws Exception {
        Peer peer = new Peer(false);
        CountDownLatch closed = new CountDownLatch(1);
        peer.close = closed::countDown;
        WebSocketServer handler = new WebSocketServer();
        try {
            handler.afterConnectionEstablished(peer.session);
            WebSocketServer.sendToUser(peer.session, "x".repeat(BoundedSessionSender.MAX_QUEUED_BYTES + 1));
            assertTrue(closed.await(1, TimeUnit.SECONDS));
            assertFalse(peer.open.get());
            assertFalse(WebSocketConcurrencyTest.<java.util.Set<?>>field("activeSessions").contains(peer.session));
        } finally { disconnect(peer); handler.statusUpdates.close(); }
    }

    @Test
    void globalByteBudgetIsSharedAcrossSessionsAndNodeCapacityIsIndependent() throws Exception {
        Peer first = new Peer(false), second = new Peer(false);
        CountDownLatch entered = new CountDownLatch(2), releaseFirst = new CountDownLatch(1), releaseSecond = new CountDownLatch(1);
        first.send = message -> { entered.countDown(); await(releaseFirst); };
        second.send = message -> { entered.countDown(); await(releaseSecond); };
        try (BoundedSessionSender sender = new BoundedSessionSender(session -> {})) {
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
            BoundedSessionSender.Write one = sender.submit(first.session, () -> "reserved payload", BoundedSessionSender.MAX_QUEUED_BYTES, deadline);
            sender.submit(second.session, () -> "reserved payload", BoundedSessionSender.MAX_QUEUED_BYTES, deadline);
            assertTrue(entered.await(1, TimeUnit.SECONDS));
            assertThrows(RejectedExecutionException.class, () -> sender.submit(new Peer(false).session, () -> "overflow", 1, deadline));
            sender.submit(new Peer(true).session, () -> "node", 4, deadline).completion.get(1, TimeUnit.SECONDS);
            releaseFirst.countDown();
            one.completion.get(1, TimeUnit.SECONDS);
            sender.submit(new Peer(false).session, () -> "reclaimed", BoundedSessionSender.MAX_QUEUED_BYTES, deadline)
                    .completion.get(1, TimeUnit.SECONDS);
        } finally { releaseFirst.countDown(); releaseSecond.countDown(); }
    }

    @Test
    void cancellingAnActiveWriteKeepsItsByteReservationUntilIoActuallyExits() throws Exception {
        Peer first = new Peer(false), second = new Peer(false);
        CountDownLatch entered = new CountDownLatch(2), release = new CountDownLatch(1), exited = new CountDownLatch(2);
        first.send = second.send = message -> { entered.countDown(); await(release); exited.countDown(); };
        // close() deliberately does not unblock this synthetic broken transport.
        try (BoundedSessionSender sender = new BoundedSessionSender(session -> {})) {
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5);
            BoundedSessionSender.Write one = sender.submit(first.session, () -> "reserved payload", BoundedSessionSender.MAX_QUEUED_BYTES, deadline);
            sender.submit(second.session, () -> "reserved payload", BoundedSessionSender.MAX_QUEUED_BYTES, deadline);
            assertTrue(entered.await(1, TimeUnit.SECONDS));
            sender.cancel(one, new TimeoutException("cancel"));
            assertTrue(one.completion.isCompletedExceptionally());
            assertThrows(RejectedExecutionException.class, () -> sender.submit(new Peer(false).session, () -> "still full", 1, deadline));
            release.countDown();
            assertTrue(exited.await(1, TimeUnit.SECONDS));
            // Waiting for the executor barrier also waits for the finally/release path.
            Field usedField = BoundedSessionSender.class.getDeclaredField("adminBytes");
            usedField.setAccessible(true);
            java.util.concurrent.atomic.AtomicLong used = (java.util.concurrent.atomic.AtomicLong) usedField.get(sender);
            long until = System.nanoTime() + TimeUnit.SECONDS.toNanos(1);
            while (used.get() != 0 && System.nanoTime() < until) Thread.yield();
            assertEquals(0, used.get());
            sender.submit(new Peer(false).session, () -> "reclaimed", BoundedSessionSender.MAX_QUEUED_BYTES, deadline)
                    .completion.get(1, TimeUnit.SECONDS);
        } finally { release.countDown(); }
    }

    @Test
    void rejectedOfflineStatusIsEventuallyRepairedWithoutAnotherNodeMessage() throws Exception {
        Peer peer = new Peer(true);
        CountDownLatch workersEntered = new CountDownLatch(2), release = new CountDownLatch(1), repaired = new CountDownLatch(1);
        WebSocketServer handler = new WebSocketServer();
        Node row = new Node();
        row.setId(peer.id);
        AtomicInteger status = new AtomicInteger(1);
        handler.nodeService = (NodeService) Proxy.newProxyInstance(getClass().getClassLoader(),
                new Class<?>[]{NodeService.class}, (proxy, method, args) -> {
                    if (method.getName().equals("list")) return List.of(row);
                    if (method.getName().equals("updateById")) {
                        status.set(((Node) args[0]).getStatus()); repaired.countDown(); return true;
                    }
                    throw new UnsupportedOperationException(method.getName());
                });
        try {
            for (int i = 0; i < 2048; i++) {
                assertTrue(handler.statusUpdates.submit("fill-" + i, () -> { workersEntered.countDown(); await(release); }));
            }
            assertTrue(workersEntered.await(1, TimeUnit.SECONDS));
            handler.afterConnectionEstablished(peer.session);
            handler.handleTransportError(peer.session, new IllegalStateException("offline while queue full"));
            assertFalse(WebSocketConcurrencyTest.<Map<Long, ?>>field("nodeSessions").containsKey(peer.id));
            release.countDown();
            long until = System.nanoTime() + TimeUnit.SECONDS.toNanos(2);
            while (handler.statusUpdates.size() > 0 && System.nanoTime() < until) Thread.yield();
            assertEquals(0, handler.statusUpdates.size());
            handler.reconcileNodeStatuses();
            assertTrue(repaired.await(1, TimeUnit.SECONDS));
            assertEquals(0, status.get());
        } finally { release.countDown(); disconnect(peer); handler.statusUpdates.close(); }
    }

    @Test
    void completedWriteReleasesCapturedPayloadEvenWhileCallerRetainsWrite() throws Exception {
        Peer peer = new Peer(true);
        try (BoundedSessionSender sender = new BoundedSessionSender(session -> {})) {
            String payload = "x".repeat(1024 * 1024);
            BoundedSessionSender.Write write = sender.submit(peer.session, () -> payload, payload.length(),
                    System.nanoTime() + TimeUnit.SECONDS.toNanos(5));
            write.completion.get(1, TimeUnit.SECONDS);
            assertNull(write.message, "a caller awaiting the response must not retain the serialized message");
            assertTrue(write.released.get());

            BoundedSessionSender.Write expired = sender.submit(peer.session, () -> payload, payload.length(),
                    System.nanoTime() - 1);
            assertTrue(expired.completion.isCompletedExceptionally());
            assertNull(expired.message);
        }
    }
}
