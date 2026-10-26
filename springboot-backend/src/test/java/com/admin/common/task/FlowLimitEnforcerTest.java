package com.admin.common.task;

import com.admin.common.dto.GostDto;
import com.admin.entity.*;
import com.admin.service.*;
import com.baomidou.mybatisplus.core.conditions.Wrapper;
import com.baomidou.mybatisplus.core.conditions.update.UpdateWrapper;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import org.springframework.test.util.ReflectionTestUtils;

import java.util.*;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

class FlowLimitEnforcerTest {
    private final UserService users = mock(UserService.class);
    private final UserTunnelService userTunnels = mock(UserTunnelService.class);
    private final ForwardService forwards = mock(ForwardService.class);
    private final TunnelService tunnels = mock(TunnelService.class);
    private final RecordingEnforcer enforcer = new RecordingEnforcer();
    private User user;

    @BeforeEach void setUp() {
        ReflectionTestUtils.setField(enforcer, "userService", users);
        ReflectionTestUtils.setField(enforcer, "userTunnelService", userTunnels);
        ReflectionTestUtils.setField(enforcer, "forwardService", forwards);
        ReflectionTestUtils.setField(enforcer, "tunnelService", tunnels);
        user = new User(); user.setStatus(0); user.setFlow(100L); user.setInFlow(0L); user.setOutFlow(0L);
        when(users.getById("7")).thenReturn(user);
        when(userTunnels.list(any(Wrapper.class))).thenReturn(List.of(permission(91, 11, 1), permission(92, 12, 1)));
        when(tunnels.getById(11)).thenReturn(tunnel(11, 1));
        when(tunnels.getById(12)).thenReturn(tunnel(12, 1));
    }
    @AfterEach void close() { enforcer.close(); }

    @Test void batchesDistinctServiceNamesAndUpdatesOnlyStatusAfterAcknowledgement() {
        when(forwards.list(any(Wrapper.class))).thenReturn(List.of(forward(101, 11), forward(102, 11)));
        enforcer.enforce("7");
        assertThat(enforcer.calls).containsExactly(List.of("101_7_91_tcp", "101_7_91_udp", "102_7_91_tcp", "102_7_91_udp"));
        ArgumentCaptor<Wrapper<Forward>> updates = ArgumentCaptor.forClass(Wrapper.class);
        verify(forwards, times(2)).update(isNull(), updates.capture());
        for (Wrapper<Forward> update : updates.getAllValues()) {
            assertThat(((UpdateWrapper<Forward>) update).getSqlSet()).startsWith("status=").doesNotContain("in_flow", "out_flow");
            assertThat(update.getSqlSegment()).contains("id", "status", "tunnel_id", "updated_time");
        }
    }

    @Test void failedRemotePauseDoesNotClaimSuccessAndRemainsRetryable() {
        when(forwards.list(any(Wrapper.class))).thenReturn(List.of(forward(101, 11)));
        when(tunnels.getById(11)).thenReturn(tunnel(11, 2));
        enforcer.failedNode = 2L;
        enforcer.enforce("7");
        assertThat(enforcer.calls).containsExactly(List.of("101_7_91_tcp", "101_7_91_udp"), List.of("101_7_91_tls"));
        verify(forwards, never()).update(any(), any(Wrapper.class));
    }

    @Test void healthyUserOnlyPausesExpiredTunnelAndUsesCurrentLimitState() {
        user.setStatus(1);
        when(userTunnels.list(any(Wrapper.class))).thenReturn(List.of(permission(91, 11, 0), permission(92, 12, 1)));
        when(forwards.list(any(Wrapper.class))).thenReturn(List.of(forward(101, 11), forward(102, 12)));
        enforcer.enforce("7");
        assertThat(enforcer.calls).containsExactly(List.of("101_7_91_tcp", "101_7_91_udp"));
        verify(forwards, times(1)).update(isNull(), any(Wrapper.class));
    }

    private static Forward forward(long id, int tunnelId) {
        Forward f = new Forward(); f.setId(id); f.setUserId(7); f.setTunnelId(tunnelId); f.setStatus(1); return f;
    }
    private static UserTunnel permission(int id, int tunnelId, int status) {
        UserTunnel p = new UserTunnel(); p.setId(id); p.setTunnelId(tunnelId); p.setUserId(7);
        p.setStatus(status); p.setFlow(100L); p.setInFlow(0L); p.setOutFlow(0L); return p;
    }
    private static Tunnel tunnel(long id, int type) {
        Tunnel t = new Tunnel(); t.setId(id); t.setType(type); t.setInNodeId(1L); t.setOutNodeId(2L); return t;
    }
    private static class RecordingEnforcer extends FlowLimitEnforcer {
        final List<List<String>> calls = new ArrayList<>();
        Long failedNode;
        @Override GostDto sendPause(Long nodeId, List<String> names, long timeoutMillis) {
            assertThat(timeoutMillis).isBetween(1L, 10000L);
            calls.add(new ArrayList<>(names));
            GostDto response = new GostDto(); response.setMsg(Objects.equals(nodeId, failedNode) ? "timeout" : "OK");
            return response;
        }
    }
}
