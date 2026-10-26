package com.admin.common.dto;

import com.fasterxml.jackson.annotation.JsonProperty;
import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import lombok.Data;

/**
 * 系统信息DTO
 * 对应Go客户端上报的系统信息结构
 */
@Data
@JsonIgnoreProperties(ignoreUnknown = true)
public class SystemInfoDto {
    
    /**
     * 主机IP地址
     */
    @JsonProperty("host_ip")
    private String hostIp;
    
    /**
     * 开机时间（秒）
     */
    @JsonProperty("uptime")
    private Long uptime;
    
    /**
     * 接收字节数
     */
    @JsonProperty("bytes_received")
    private Long bytesReceived;
    
    /**
     * 发送字节数
     */
    @JsonProperty("bytes_transmitted")
    private Long bytesTransmitted;
    
    /**
     * CPU使用率（百分比）
     */
    @JsonProperty("cpu_usage")
    private Double cpuUsage;
    
    /**
     * 内存使用率（百分比）
     */
    @JsonProperty("memory_usage")
    private Double memoryUsage;

    /** Optional: older agents do not report congestion telemetry. */
    private Congestion congestion;

    /** Null kernel values mean unavailable, never a measured zero. */
    @Data
    @JsonIgnoreProperties(ignoreUnknown = true)
    public static class Congestion {
        @JsonProperty("observed_at")
        private Long observedAt;
        @JsonProperty("active_transfers")
        private Long activeTransfers;
        @JsonProperty("bytes_forwarded_total")
        private Long bytesForwardedTotal;
        @JsonProperty("write_stalls_total")
        private Long writeStallsTotal;
        @JsonProperty("write_timeouts_total")
        private Long writeTimeoutsTotal;
        @JsonProperty("active_stalled_writes")
        private Long activeStalledWrites;
        @JsonProperty("max_write_block_ms")
        private Double maxWriteBlockMs;
        @JsonProperty("tcp_sampling_supported")
        private Boolean tcpSamplingSupported;
        @JsonProperty("tcp_sampling_limited")
        private Boolean tcpSamplingLimited;
        @JsonProperty("tcp_tracked_connections")
        private Long tcpTrackedConnections;
        @JsonProperty("tcp_sampled_connections")
        private Long tcpSampledConnections;
        @JsonProperty("tcp_unavailable_connections")
        private Long tcpUnavailableConnections;
        @JsonProperty("tcp_sample_errors")
        private Long tcpSampleErrors;
        @JsonProperty("tcp_rtt_ms_avg")
        private Double tcpRttMsAvg;
        @JsonProperty("tcp_min_rtt_ms")
        private Double tcpMinRttMs;
        // Aggregates cover the currently sampled sockets, not the node's lifetime.
        @JsonProperty("tcp_retransmissions")
        private Long tcpRetransmissions;
        @JsonProperty("tcp_notsent_bytes")
        private Long tcpNotsentBytes;
        // Bits per second; kernel estimate, not available node bandwidth.
        @JsonProperty("tcp_delivery_rate_bps")
        private Double tcpDeliveryRateBps;
        @JsonProperty("tcp_bbr_connections")
        private Long tcpBbrConnections;
        @JsonProperty("tcp_cubic_connections")
        private Long tcpCubicConnections;
    }
    
    /**
     * 上报时间戳
     */
    private Long timestamp;
    
    public SystemInfoDto() {
        this.timestamp = System.currentTimeMillis();
    }
}
