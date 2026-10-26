package com.admin.config;

import com.admin.common.utils.AESCrypto;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Component;

import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 加密配置管理类
 * 统一管理AES加密功能，为HTTP上报和WebSocket通信提供加密支持
 */
@Component
@Slf4j
public class EncryptionConfig {
    
    // 缓存加密器实例，避免重复创建
    static final int MAX_CACHE_SIZE = 256;
    private static final Map<String, AESCrypto> CRYPTO_CACHE = new LinkedHashMap<>(32, 0.75f, true);
    
    /**
     * 获取或创建AES加密器实例
     * 
     * @param secret 密钥字符串
     * @return AES加密器实例，如果创建失败返回null
     */
    public static synchronized AESCrypto getOrCreateCrypto(String secret) {
        if (secret == null || secret.isEmpty()) {
            return null;
        }
        AESCrypto crypto = CRYPTO_CACHE.get(secret);
        if (crypto == null) {
            crypto = AESCrypto.create(secret);
            if (crypto != null) {
                if (CRYPTO_CACHE.size() >= MAX_CACHE_SIZE) {
                    CRYPTO_CACHE.remove(CRYPTO_CACHE.keySet().iterator().next());
                }
                CRYPTO_CACHE.put(secret, crypto);
            }
        }
        return crypto;
    }
    
    /**
     * 检测消息是否为加密格式
     * 
     * @param message 消息内容
     * @return 如果是加密格式返回true，否则返回false
     */
    public static boolean isEncryptedMessage(String message) {
        if (message == null || message.trim().isEmpty()) {
            return false;
        }
        
        try {
            // 简单检查是否包含加密标识
            return message.contains("\"encrypted\":true") || message.contains("\"encrypted\": true");
        } catch (Exception e) {
            return false;
        }
    }
    
    /**
     * 清理缓存的加密器实例
     * 
     * @param secret 要清理的密钥
     */
    public static synchronized void clearCrypto(String secret) {
        if (secret != null) {
            CRYPTO_CACHE.remove(secret);
            log.info("已清理密钥对应的加密器实例");
        }
    }
    
    /**
     * 清理所有缓存的加密器实例
     */
    public static synchronized void clearAllCrypto() {
        int size = CRYPTO_CACHE.size();
        CRYPTO_CACHE.clear();
        log.info("已清理所有加密器实例缓存，共清理 {} 个实例", size);
    }
    
    /**
     * 获取当前缓存的加密器数量
     * 
     * @return 缓存的加密器数量
     */
    public static synchronized int getCacheSize() {
        return CRYPTO_CACHE.size();
    }
}
