package com.admin.common.utils;

import java.util.HashMap;
import java.util.Map;
import java.util.Objects;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Consumer;

/** Bounded work with one running task and at most one latest replacement per key. */
public final class BoundedKeyedExecutor implements AutoCloseable {
    private final Map<String, Slot> slots = new HashMap<>();
    private final ThreadPoolExecutor executor;
    private final int capacity;
    private final Consumer<RuntimeException> onFailure;
    private boolean closed;

    public BoundedKeyedExecutor(String name, int workers, int capacity,
                                Consumer<RuntimeException> onFailure) {
        if (workers < 1 || capacity < workers) throw new IllegalArgumentException("invalid capacity");
        this.capacity = capacity;
        this.onFailure = Objects.requireNonNull(onFailure);
        AtomicInteger ids = new AtomicInteger();
        // The number of slots, including running ones, is bounded by capacity.
        // A running slot can always enqueue its replacement without growing the queue.
        executor = new ThreadPoolExecutor(workers, workers, 0, TimeUnit.MILLISECONDS,
                new ArrayBlockingQueue<>(capacity), runnable -> {
                    Thread thread = new Thread(runnable, name + "-" + ids.incrementAndGet());
                    thread.setDaemon(true);
                    return thread;
                }, new ThreadPoolExecutor.AbortPolicy());
    }

    public synchronized boolean submit(String key, Runnable task) {
        Objects.requireNonNull(key);
        Objects.requireNonNull(task);
        if (closed) return false;
        Slot existing = slots.get(key);
        if (existing != null) {
            existing.latest = task;
            return true;
        }
        if (slots.size() >= capacity) return false;
        Slot slot = new Slot(key, task);
        slots.put(key, slot);
        try {
            executor.execute(slot);
            return true;
        } catch (RejectedExecutionException e) {
            slots.remove(key);
            return false;
        }
    }

    public synchronized int size() {
        return slots.size();
    }

    @Override
    public synchronized void close() {
        closed = true;
        slots.values().forEach(slot -> slot.latest = null);
        slots.clear();
        executor.shutdownNow();
    }

    private final class Slot implements Runnable {
        private final String key;
        private Runnable latest;

        private Slot(String key, Runnable task) {
            this.key = key;
            latest = task;
        }

        @Override
        public void run() {
            Runnable task;
            synchronized (BoundedKeyedExecutor.this) {
                if (closed) return;
                task = latest;
                latest = null;
            }
            try {
                task.run();
            } catch (RuntimeException e) {
                onFailure.accept(e);
            } finally {
                synchronized (BoundedKeyedExecutor.this) {
                    if (!closed && latest != null) {
                        // Requeue at the tail so a noisy key cannot monopolize a worker.
                        executor.execute(this);
                    } else {
                        slots.remove(key, this);
                    }
                }
            }
        }
    }
}
