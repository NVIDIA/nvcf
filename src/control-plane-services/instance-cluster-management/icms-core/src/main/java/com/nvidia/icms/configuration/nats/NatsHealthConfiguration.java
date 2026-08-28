package com.nvidia.icms.configuration.nats;

import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.health.autoconfigure.contributor.CompositeHealthContributorConfiguration;
import org.springframework.boot.health.contributor.AbstractHealthIndicator;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthContributor;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration(proxyBeanMethods = false)
@ConditionalOnProperty(prefix = "icms.nats", name = "nats-enabled", havingValue = "true")
public class NatsHealthConfiguration extends
        CompositeHealthContributorConfiguration<NatsHealthConfiguration.NatsHealthIndicator, FixedNatsPool> {

    NatsHealthConfiguration() {
        super(NatsHealthIndicator::new);
    }

    @RequiredArgsConstructor
    public static class NatsHealthIndicator extends AbstractHealthIndicator {

        private final FixedNatsPool connection;

        @Override
        protected void doHealthCheck(Health.Builder builder) {
            if (connection.healthy()) {
                builder.up();
            } else {
                builder.down();
            }
        }
    }

    @Bean
    HealthContributor natsHealthContributor(Map<String, FixedNatsPool> connections) {
        return createContributor(connections);
    }
}
