package com.admin.controller;

import com.admin.common.dto.FlowDto;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;

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
}
