package com.admin.config;

import com.admin.common.utils.AESCrypto;
import org.junit.jupiter.api.Test;

import java.util.concurrent.*;

import static org.junit.jupiter.api.Assertions.*;

class EncryptionConfigTest {
    @Test
    void cacheIsBoundedAndEvictedKeysStillDecryptExistingMessages() throws Exception {
        EncryptionConfig.clearAllCrypto();
        AESCrypto original = EncryptionConfig.getOrCreateCrypto("synthetic-original");
        String ciphertext = original.encrypt("payload");
        for (int i = 0; i < 1000; i++) EncryptionConfig.getOrCreateCrypto("synthetic-" + i);
        assertEquals(EncryptionConfig.MAX_CACHE_SIZE, EncryptionConfig.getCacheSize());
        AESCrypto recreated = EncryptionConfig.getOrCreateCrypto("synthetic-original");
        assertNotSame(original, recreated);
        assertEquals("payload", recreated.decryptString(ciphertext));
        assertSame(recreated, EncryptionConfig.getOrCreateCrypto("synthetic-original"));
        EncryptionConfig.clearAllCrypto();
    }

    @Test
    void concurrentKeyChurnCannotExceedCacheCapacity() throws Exception {
        EncryptionConfig.clearAllCrypto();
        ExecutorService workers = Executors.newFixedThreadPool(8);
        try {
            CompletableFuture<?>[] calls = new CompletableFuture<?>[8];
            for (int worker = 0; worker < calls.length; worker++) {
                int id = worker;
                calls[worker] = CompletableFuture.runAsync(() -> {
                    for (int i = 0; i < 300; i++) EncryptionConfig.getOrCreateCrypto("synthetic-" + id + "-" + i);
                }, workers);
            }
            CompletableFuture.allOf(calls).get(5, TimeUnit.SECONDS);
            assertEquals(EncryptionConfig.MAX_CACHE_SIZE, EncryptionConfig.getCacheSize());
        } finally { workers.shutdownNow(); EncryptionConfig.clearAllCrypto(); }
    }
}
