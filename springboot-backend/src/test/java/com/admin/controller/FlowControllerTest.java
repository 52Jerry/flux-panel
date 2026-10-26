package com.admin.controller;

import com.admin.common.dto.FlowDto;
import com.admin.common.task.FlowLimitEnforcer;
import com.admin.service.*;
import com.baomidou.mybatisplus.core.conditions.Wrapper;
import org.junit.jupiter.api.Test;
import org.springframework.test.util.ReflectionTestUtils;

import java.lang.reflect.Method;
import java.util.Collections;
import java.util.IdentityHashMap;
import java.util.List;
import java.util.Set;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.*;
import static org.mockito.Mockito.*;

class FlowControllerTest {

    @Test
    void parseFlowPayloadAcceptsCurrentObjectReport() {
        List<FlowDto> result = FlowController.parseFlowPayload(
                "{\"n\":\"forward-user-tunnel\",\"u\":100,\"d\":200}"
        );

        assertThat(result).hasSize(1);
        assertThat(result.get(0).getN()).isEqualTo("forward-user-tunnel");
        assertThat(result.get(0).getU()).isEqualTo(100);
        assertThat(result.get(0).getD()).isEqualTo(200);
    }

    @Test
    void parseFlowPayloadAcceptsLegacyArrayReport() {
        List<FlowDto> result = FlowController.parseFlowPayload(
                "[{\"n\":\"web_api\",\"u\":1,\"d\":2},{\"n\":\"forward_1_1_0\",\"u\":3,\"d\":4}]"
        );

        assertThat(result).hasSize(2);
        assertThat(result.get(0).getN()).isEqualTo("web_api");
        assertThat(result.get(1).getN()).isEqualTo("forward_1_1_0");
    }

    @Test
    void parseFlowPayloadTreatsEmptyPayloadAsNoReports() {
        assertThat(FlowController.parseFlowPayload(" ")).isEmpty();
    }

    @Test
    void historicalIdsHaveBoundedLocksAndTheSameIdKeepsTheSameLock() throws Exception {
        FlowController controller = new FlowController();
        for (String name : List.of("getUserLock", "getTunnelLock", "getForwardLock")) {
            Method method = FlowController.class.getDeclaredMethod(name, String.class);
            method.setAccessible(true);
            Object original = method.invoke(controller, "stable-id");
            Set<Object> retained = Collections.newSetFromMap(new IdentityHashMap<>());
            for (int i = 0; i < 20000; i++) retained.add(method.invoke(controller, "historical-" + i));
            assertThat(retained.size()).isLessThanOrEqualTo(256);
            assertThat(method.invoke(controller, "stable-id")).isSameAs(original);
        }
    }

    @Test
    void batchAccountsForEveryRecordBeforeSchedulingOneCheckPerUser() {
        FlowController controller = new FlowController();
        NodeService nodes = mock(NodeService.class);
        ForwardService forwards = mock(ForwardService.class);
        UserService users = mock(UserService.class);
        UserTunnelService permissions = mock(UserTunnelService.class);
        FlowLimitEnforcer limits = mock(FlowLimitEnforcer.class);
        ReflectionTestUtils.setField(controller, "nodeService", nodes);
        ReflectionTestUtils.setField(controller, "forwardService", forwards);
        ReflectionTestUtils.setField(controller, "userService", users);
        ReflectionTestUtils.setField(controller, "userTunnelService", permissions);
        ReflectionTestUtils.setField(controller, "flowLimitEnforcer", limits);
        when(nodes.count(any(Wrapper.class))).thenReturn(1);
        assertThat(controller.uploadFlowData("[{\"n\":\"101_7_91_tcp\",\"u\":1,\"d\":2},"
                + "{\"n\":\"102_7_91_tcp\",\"u\":3,\"d\":4}]", "test-secret")).isEqualTo("ok");
        var order = inOrder(forwards, users, permissions, limits);
        for (int i = 0; i < 2; i++) {
            order.verify(forwards).update(isNull(), any(Wrapper.class));
            order.verify(users).update(isNull(), any(Wrapper.class));
            order.verify(permissions).update(isNull(), any(Wrapper.class));
        }
        order.verify(limits).schedule("7");
        verifyNoMoreInteractions(limits);
    }
}
