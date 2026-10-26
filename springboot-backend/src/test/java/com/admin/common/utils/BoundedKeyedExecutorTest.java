package com.admin.common.utils;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

import static org.assertj.core.api.Assertions.assertThat;

class BoundedKeyedExecutorTest {
    @Test
    void coalescesLatestWorkWithinCapacityAndGivesOtherKeysATurn() throws Exception {
        List<String> order = new CopyOnWriteArrayList<>();
        CountDownLatch entered = new CountDownLatch(1), release = new CountDownLatch(1), done = new CountDownLatch(2);
        try (BoundedKeyedExecutor executor = new BoundedKeyedExecutor("test", 1, 2, e -> { throw e; })) {
            assertThat(executor.submit("a", () -> { order.add("a0"); entered.countDown(); await(release); })).isTrue();
            assertThat(entered.await(2, TimeUnit.SECONDS)).isTrue();
            assertThat(executor.submit("b", () -> { order.add("b"); done.countDown(); })).isTrue();
            assertThat(executor.submit("a", () -> order.add("obsolete"))).isTrue();
            assertThat(executor.submit("a", () -> { order.add("latest"); done.countDown(); })).isTrue();
            assertThat(executor.submit("c", () -> order.add("overflow"))).isFalse();
            assertThat(executor.size()).isEqualTo(2);
            release.countDown();
            assertThat(done.await(2, TimeUnit.SECONDS)).isTrue();
            assertThat(order).containsExactly("a0", "b", "latest");
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(2);
            while (executor.size() != 0 && System.nanoTime() < deadline) Thread.sleep(1);
            assertThat(executor.size()).isZero();
        } finally {
            release.countDown();
        }
    }

    @Test
    void shutdownInterruptsRunningWorkAndDropsQueuedReferences() throws Exception {
        CountDownLatch entered = new CountDownLatch(1), interrupted = new CountDownLatch(1);
        List<String> ran = new CopyOnWriteArrayList<>();
        BoundedKeyedExecutor executor = new BoundedKeyedExecutor("test-close", 1, 2, e -> { throw e; });
        try {
            executor.submit("a", () -> {
                entered.countDown();
                try { new CountDownLatch(1).await(); }
                catch (InterruptedException e) { interrupted.countDown(); Thread.currentThread().interrupt(); }
            });
            assertThat(entered.await(2, TimeUnit.SECONDS)).isTrue();
            executor.submit("b", () -> ran.add("b"));
            executor.close();
            assertThat(interrupted.await(2, TimeUnit.SECONDS)).isTrue();
            assertThat(executor.size()).isZero();
            assertThat(executor.submit("c", () -> ran.add("c"))).isFalse();
            assertThat(ran).isEmpty();
        } finally { executor.close(); }
    }

    private static void await(CountDownLatch latch) {
        try { latch.await(); }
        catch (InterruptedException e) { Thread.currentThread().interrupt(); }
    }
}
