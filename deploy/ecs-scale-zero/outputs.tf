output "url" { value = aws_apigatewayv2_stage.edge.invoke_url }
output "cluster_arn" { value = aws_ecs_cluster.this.arn }
output "task_definition_arn" { value = aws_ecs_task_definition.application.arn }
output "activator_function_name" { value = aws_lambda_function.activator.function_name }
output "websocket_endpoint" { value = "wss://${aws_lb.websocket.dns_name}:${var.websocket_listener_port}" }
output "websocket_application_service_arn" { value = aws_ecs_service.websocket_application.id }
output "websocket_edge_service_arn" { value = aws_ecs_service.websocket_edge.id }
