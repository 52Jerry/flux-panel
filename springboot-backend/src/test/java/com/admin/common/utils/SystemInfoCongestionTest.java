package com.admin.common.utils;

import com.admin.common.dto.SystemInfoDto;
import com.admin.config.EncryptionConfig;
import com.alibaba.fastjson.JSON;
import com.alibaba.fastjson.JSONObject;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;
import org.springframework.web.socket.CloseStatus;
import org.springframework.web.socket.TextMessage;
import org.springframework.web.socket.WebSocketSession;

import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.jupiter.api.Assertions.*;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.*;

class SystemInfoCongestionTest {
    private static final AtomicInteger ids = new AtomicInteger(30000);
    private final ObjectMapper mapper = new ObjectMapper();

    @Test
    void dtoAcceptsLegacyReportsAndPreservesUnavailableValues() throws Exception {
        assertNull(mapper.readValue("{\"memory_usage\":12.5}", SystemInfoDto.class).getCongestion());
        SystemInfoDto info = mapper.readValue("{\"memory_usage\":12.5,\"congestion\":{"
                + "\"observed_at\":1793000000000,\"active_transfers\":0,\"tcp_sampling_supported\":false,"
                + "\"tcp_rtt_ms_avg\":null,\"tcp_retransmissions\":null,\"future_field\":true}}", SystemInfoDto.class);
        assertEquals(0L, info.getCongestion().getActiveTransfers());
        assertFalse(info.getCongestion().getTcpSamplingSupported());
        assertNull(info.getCongestion().getTcpRttMsAvg());
        assertNull(info.getCongestion().getTcpRetransmissions());
        var encoded = mapper.valueToTree(info).get("congestion");
        assertEquals(1793000000000L, encoded.get("observed_at").longValue());
        assertTrue(encoded.get("tcp_rtt_ms_avg").isNull());
    }

    @Test
    void plaintextLegacyAndEncryptedTelemetryPassThroughUnchanged() throws Exception {
        WebSocketServer handler = new WebSocketServer();
        WebSocketSession admin = peer("0");
        WebSocketSession node = peer("1");
        String secret = "synthetic-congestion-test";
        node.getAttributes().put("nodeSecret", secret);
        LinkedBlockingQueue<String> received = new LinkedBlockingQueue<>();
        doAnswer(call -> {
            received.add(((TextMessage) call.getArgument(0)).getPayload());
            return null;
        }).when(admin).sendMessage(any());
        handler.afterConnectionEstablished(admin);
        try {
            String legacy = "{\"memory_usage\":1,\"uptime\":2}";
            handler.handleTextMessage(node, new TextMessage(legacy));
            assertReport(received, legacy);

            String report = "{\"memory_usage\":1,\"congestion\":{\"active_transfers\":2,"
                    + "\"tcp_sampling_supported\":true,\"tcp_sampling_limited\":true,"
                    + "\"tcp_sampled_connections\":1,\"tcp_rtt_ms_avg\":17.5,\"tcp_notsent_bytes\":null,"
                    + "\"tcp_retransmissions\":0,\"future_metric\":42}}";
            JSONObject wrapper = new JSONObject();
            wrapper.put("encrypted", true);
            wrapper.put("data", EncryptionConfig.getOrCreateCrypto(secret).encrypt(report));
            handler.handleTextMessage(node, new TextMessage(wrapper.toJSONString()));
            assertReport(received, report);
        } finally {
            handler.afterConnectionClosed(admin, CloseStatus.NORMAL);
            handler.afterConnectionClosed(node, CloseStatus.NORMAL);
            handler.statusUpdates.close();
        }
    }

    private static void assertReport(LinkedBlockingQueue<String> received, String expected) throws Exception {
        String message = received.poll(2, TimeUnit.SECONDS);
        assertNotNull(message, "telemetry was not broadcast");
        JSONObject envelope = JSON.parseObject(message);
        assertEquals("info", envelope.getString("type"));
        assertEquals(expected, envelope.getString("data"));
    }

    private static WebSocketSession peer(String type) {
        WebSocketSession session = mock(WebSocketSession.class);
        String id = String.valueOf(ids.incrementAndGet());
        Map<String, Object> attributes = new HashMap<>();
        attributes.put("id", id);
        attributes.put("type", type);
        when(session.getId()).thenReturn(id);
        when(session.getAttributes()).thenReturn(attributes);
        when(session.isOpen()).thenReturn(true);
        return session;
    }
}
