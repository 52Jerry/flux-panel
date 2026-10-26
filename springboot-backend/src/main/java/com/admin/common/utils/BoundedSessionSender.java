package com.admin.common.utils;

import org.springframework.web.socket.CloseStatus;
import org.springframework.web.socket.TextMessage;
import org.springframework.web.socket.WebSocketSession;

import java.io.IOException;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Map;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.Consumer;
import java.util.function.Supplier;

/** Serializes writes per connection without blocking a WebSocket receive thread. */
final class BoundedSessionSender implements AutoCloseable {
    static final int MAX_MESSAGES = 128;
    static final int MAX_QUEUED_BYTES = 4 * 1024 * 1024;
    static final long MAX_NODE_BYTES = 32L * 1024 * 1024;
    static final long MAX_ADMIN_BYTES = 8L * 1024 * 1024;
    private static final String DISABLED_ATTRIBUTE = BoundedSessionSender.class.getName() + ".closed";
    private final Map<WebSocketSession, SessionQueue> sessions = new ConcurrentHashMap<>();
    private final AtomicLong nodeBytes = new AtomicLong();
    private final AtomicLong adminBytes = new AtomicLong();
    private final ThreadPoolExecutor nodeWorkers = pool("flux-node-send", 8, 256);
    private final ThreadPoolExecutor adminWorkers = pool("flux-admin-send", 4, 256);
    private final ThreadPoolExecutor closeWorkers = pool("flux-ws-close", 2, 256);
    private final ScheduledExecutorService watchdog = Executors.newSingleThreadScheduledExecutor(threads("flux-ws-watchdog"));
    private final Consumer<WebSocketSession> onFailure;
    private volatile boolean closed;

    BoundedSessionSender(Consumer<WebSocketSession> onFailure) {
        this.onFailure = onFailure;
        watchdog.scheduleWithFixedDelay(this::expireWrites, 1, 1, TimeUnit.SECONDS);
    }

    static final class Write {
        final CompletableFuture<Void> completion = new CompletableFuture<>();
        volatile Supplier<String> message;
        final long deadline;
        final int bytes;
        final SessionQueue owner;
        final AtomicLong budget;
        final AtomicBoolean released = new AtomicBoolean();

        Write(SessionQueue owner, Supplier<String> message, int bytes, long deadline, AtomicLong budget) {
            this.owner = owner;
            this.message = message;
            this.bytes = bytes;
            this.deadline = deadline;
            this.budget = budget;
        }

        void release() {
            // send_msg retains this Write while awaiting its response. Drop the
            // captured JSON before releasing its byte reservation, even then.
            // Active writes only reach release after sendMessage has exited.
            message = null;
            if (released.compareAndSet(false, true)) budget.addAndGet(-bytes);
        }
    }

    private static final class SessionQueue {
        final WebSocketSession session;
        final ArrayDeque<Write> queue = new ArrayDeque<>();
        int bytes;
        boolean draining;
        boolean closed;
        Write current;

        SessionQueue(WebSocketSession session) { this.session = session; }
    }

    Write submit(WebSocketSession session, Supplier<String> message, int bytes, long deadline) {
        if (closed || session == null || !session.isOpen() || isDisabled(session)) {
            throw new RejectedExecutionException("连接已断开");
        }
        boolean node = "1".equals(session.getAttributes().get("type"));
        AtomicLong budget = node ? nodeBytes : adminBytes;
        if (bytes < 0 || bytes > MAX_QUEUED_BYTES || !reserve(budget, bytes, node ? MAX_NODE_BYTES : MAX_ADMIN_BYTES)) {
            RejectedExecutionException error = new RejectedExecutionException("发送缓冲区已满");
            disconnect(session, error);
            throw error;
        }
        SessionQueue state = sessions.computeIfAbsent(session, SessionQueue::new);
        Write write = new Write(state, message, bytes, deadline, budget);
        boolean rejected = false;
        synchronized (state) {
            if (deadline - System.nanoTime() <= 0) {
                write.completion.completeExceptionally(new TimeoutException("发送超时"));
                write.release();
                return write;
            }
            if (closed || state.closed || !session.isOpen() || isDisabled(session)
                    || bytes > MAX_QUEUED_BYTES || state.queue.size() >= MAX_MESSAGES
                    || state.bytes > MAX_QUEUED_BYTES - bytes) {
                rejected = true;
            } else {
                state.queue.addLast(write);
                state.bytes += bytes;
                if (!state.draining) {
                    state.draining = true;
                    try {
                        workers(session).execute(() -> drain(state));
                    } catch (RejectedExecutionException e) {
                        rejected = true;
                    }
                }
            }
        }
        if (rejected) {
            fail(state, new RejectedExecutionException("发送队列已满或连接已关闭"));
            write.release();
            throw new RejectedExecutionException("发送队列已满或连接已关闭");
        }
        return write;
    }

    private ThreadPoolExecutor workers(WebSocketSession session) {
        return "1".equals(session.getAttributes().get("type")) ? nodeWorkers : adminWorkers;
    }

    private void drain(SessionQueue state) {
        while (true) {
            Write write;
            synchronized (state) {
                if (state.closed || (write = state.queue.pollFirst()) == null) {
                    state.draining = false;
                    return;
                }
                state.bytes -= write.bytes;
                state.current = write;
            }
            try {
                if (!write.completion.isDone()) {
                    String payload = write.message.get();
                    // Encryption and executor wait both consume the same deadline.
                    if (write.deadline - System.nanoTime() <= 0) {
                        write.release();
                        write.completion.completeExceptionally(new TimeoutException("发送超时"));
                    } else if (!write.completion.isDone()) {
                        if (!state.session.isOpen()) throw new IOException("连接已断开");
                        state.session.sendMessage(new TextMessage(payload));
                        write.release();
                        write.completion.complete(null);
                    }
                }
            } catch (Exception e) {
                write.release();
                write.completion.completeExceptionally(e);
                fail(state, e);
                return;
            } finally {
                synchronized (state) { state.current = null; }
                write.release();
            }
        }
    }

    /** Queued commands are removed; an already completed send is never disconnected. */
    void cancel(Write write, Throwable reason) {
        if (write == null) return;
        boolean sending;
        synchronized (write.owner) {
            if (!write.completion.completeExceptionally(reason)) return;
            if (write.owner.queue.remove(write)) {
                write.owner.bytes -= write.bytes;
                write.release();
            }
            sending = write.owner.current == write;
        }
        if (sending) fail(write.owner, reason);
    }

    void remove(WebSocketSession session, Throwable reason) {
        synchronized (session) { session.getAttributes().put(DISABLED_ATTRIBUTE, true); }
        SessionQueue state = sessions.remove(session);
        if (state != null) stop(state, reason);
    }

    void disconnect(WebSocketSession session, Throwable reason) {
        if (session != null) fail(sessions.computeIfAbsent(session, SessionQueue::new), reason);
    }

    private boolean stop(SessionQueue state, Throwable reason) {
        ArrayList<Write> abandoned;
        Write current;
        synchronized (state) {
            if (state.closed) return false;
            state.closed = true;
            abandoned = new ArrayList<>(state.queue);
            state.queue.clear();
            state.bytes = 0;
            current = state.current;
        }
        abandoned.forEach(write -> {
            write.completion.completeExceptionally(reason);
            write.release();
        });
        // A cancelled socket write still retains its payload until sendMessage exits.
        if (current != null) current.completion.completeExceptionally(reason);
        sessions.remove(state.session, state);
        return true;
    }

    private void fail(SessionQueue state, Throwable reason) {
        synchronized (state.session) { state.session.getAttributes().put(DISABLED_ATTRIBUTE, true); }
        if (!stop(state, reason)) return;
        onFailure.accept(state.session);
        // Closing a broken transport can also block. Keep it off receiver/watchdog threads.
        try {
            closeWorkers.execute(() -> {
                try { state.session.close(CloseStatus.SESSION_NOT_RELIABLE); }
                catch (Exception ignored) { /* The transport may have closed already. */ }
            });
        } catch (RejectedExecutionException ignored) {
            // References and pending commands are already released; container timeout owns I/O.
        }
    }

    private static boolean isDisabled(WebSocketSession session) {
        synchronized (session) { return Boolean.TRUE.equals(session.getAttributes().get(DISABLED_ATTRIBUTE)); }
    }

    private static boolean reserve(AtomicLong used, long bytes, long limit) {
        long current;
        do {
            current = used.get();
            if (current > limit - bytes) return false;
        } while (!used.compareAndSet(current, current + bytes));
        return true;
    }

    private void expireWrites() {
        long now = System.nanoTime();
        for (SessionQueue state : sessions.values()) {
            Write current;
            synchronized (state) { current = state.current; }
            if (current != null && current.deadline - now <= 0) {
                cancel(current, new TimeoutException("发送超时"));
            }
        }
    }

    private static ThreadFactory threads(String prefix) {
        AtomicInteger sequence = new AtomicInteger();
        return task -> {
            Thread thread = new Thread(task, prefix + "-" + sequence.incrementAndGet());
            thread.setDaemon(true);
            return thread;
        };
    }

    private static ThreadPoolExecutor pool(String name, int size, int capacity) {
        return new ThreadPoolExecutor(size, size, 30, TimeUnit.SECONDS,
                new ArrayBlockingQueue<>(capacity), threads(name), new ThreadPoolExecutor.AbortPolicy());
    }

    @Override
    public void close() {
        closed = true;
        sessions.values().forEach(state -> fail(state, new IOException("服务正在关闭")));
        watchdog.shutdownNow();
        nodeWorkers.shutdownNow();
        adminWorkers.shutdownNow();
        closeWorkers.shutdown();
    }
}
